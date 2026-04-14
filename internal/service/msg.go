package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pb "game-im/api/pb"
	"game-im/internal/delivery"
	"game-im/internal/store"
	"game-im/pkg/errs"
)

const (
	maxMsgLength  = 1024 // bytes
	maxPullLimit  = 50
	ratePerSecond = 100 // per user per second
)

type SendMsgReq struct {
	ChannelID   string
	ChannelType pb.ChannelType
	Content     string
	MsgType     pb.MsgType
	MsgParam    []string
	ClientMsgID string
}

type SendMsgResp struct {
	MsgID    int64
	SendTime int64
}

type MsgService interface {
	SendMsg(ctx context.Context, uid int64, sender *pb.SenderInfo, req *SendMsgReq) (*SendMsgResp, error)
	PullMsg(ctx context.Context, channelID string, lastMsgID int64, limit int) ([]*pb.ImMessage, error)
	AckMsg(ctx context.Context, uid int64, channelID string, msgID int64) error
}

type msgService struct {
	redis      *store.RedisStore
	mongo      *store.MongoStore
	ban        BanService
	channels   ChannelService
	filter     *FilterChain
	engine     *delivery.Engine
	seqAlloc   *SeqAllocator
	banCache   *LocalCache // 5s TTL cache for ban checks
	availCache *LocalCache // 5s TTL cache for channel availability
	writeCh    chan *store.MessageDoc
	logger     *slog.Logger
}

func NewMsgService(
	redis *store.RedisStore,
	mongo *store.MongoStore,
	ban BanService,
	channels ChannelService,
	filter *FilterChain,
	engine *delivery.Engine,
	logger *slog.Logger,
) MsgService {
	s := &msgService{
		redis:      redis,
		mongo:      mongo,
		ban:        ban,
		channels:   channels,
		filter:     filter,
		engine:     engine,
		seqAlloc:   NewSeqAllocator(redis),
		banCache:   NewLocalCache(5 * time.Second),
		availCache: NewLocalCache(5 * time.Second),
		writeCh:    make(chan *store.MessageDoc, 4096),
		logger:     logger,
	}
	go s.asyncWriter()
	return s
}

func (s *msgService) SendMsg(ctx context.Context, uid int64, sender *pb.SenderInfo, req *SendMsgReq) (*SendMsgResp, error) {
	// 1. CPU-only checks (no I/O)
	if len(req.Content) > maxMsgLength {
		return nil, errs.ErrMsgTooLong
	}
	filterResult := s.filter.Check(uid, req.Content)
	if !filterResult.Pass {
		return nil, errs.ErrContentIllegal
	}
	content := req.Content
	if filterResult.Replace != "" {
		content = filterResult.Replace
	}

	// 2. Local cache checks — ban + channel availability (0 Redis RTT)
	if banned := s.checkBanCached(ctx, uid); banned {
		return nil, errs.ErrUserBanned
	}
	if avail := s.checkAvailCached(ctx, req.ChannelType); !avail {
		return nil, errs.ErrChannelUnavail
	}

	// 3. Allocate seq from memory (0 Redis RTT on hit, 1 RTT on batch refill)
	msgID, err := s.seqAlloc.Next(ctx, req.ChannelID)
	if err != nil {
		return nil, errs.ErrServerError
	}

	// 4. Dedup + rate limit in slim Lua (1 Redis RTT, only 2-3 operations)
	check, err := s.redis.DedupAndRateCheck(ctx, req.ClientMsgID, uid, ratePerSecond, msgID)
	if err != nil {
		return nil, errs.ErrServerError
	}
	if check.DedupExists {
		return &SendMsgResp{MsgID: check.DedupMsgID, SendTime: time.Now().UnixMilli()}, nil
	}
	if !check.RateOK {
		return nil, errs.ErrRateLimited
	}
	sendTime := time.Now().UnixMilli()

	// 3. Build message
	msg := &pb.ImMessage{
		MsgId:       msgID,
		ChannelId:   req.ChannelID,
		ChannelType: req.ChannelType,
		Sender:      sender,
		Content:     content,
		MsgType:     req.MsgType,
		MsgParam:    req.MsgParam,
		SendTime:    sendTime,
		ClientMsgId: req.ClientMsgID,
	}

	// 4. Post-send: cache write — ASYNC, does not block response
	//    (dedup is already set atomically in the Lua script)
	s.redis.PostSendAsync(req.ChannelID, msg)

	// 5. Async DB write (buffered channel, non-blocking)
	senderID := int64(0)
	senderName := ""
	if sender != nil {
		senderID = sender.SenderId
		senderName = sender.SenderName
	}
	select {
	case s.writeCh <- &store.MessageDoc{
		MsgID:       msgID,
		ChannelID:   req.ChannelID,
		ChannelType: int32(req.ChannelType),
		SenderID:    senderID,
		SenderName:  senderName,
		Content:     content,
		MsgType:     int32(req.MsgType),
		MsgParam:    req.MsgParam,
		SendTime:    sendTime,
		CreatedAt:   time.Now(),
	}:
	default:
		s.logger.Warn("write channel full, dropping message for async write")
	}

	// 6. Deliver (world/system are async via bus goroutine)
	if err := s.engine.Deliver(ctx, msg); err != nil {
		s.logger.Error("delivery failed", "msgId", msgID, "err", err)
	}

	return &SendMsgResp{MsgID: msgID, SendTime: sendTime}, nil
}

func (s *msgService) PullMsg(ctx context.Context, channelID string, lastMsgID int64, limit int) ([]*pb.ImMessage, error) {
	if limit <= 0 || limit > maxPullLimit {
		limit = maxPullLimit
	}

	// Try Redis cache first
	msgs, err := s.redis.GetMsgCache(ctx, channelID, lastMsgID, limit)
	if err != nil {
		s.logger.Warn("msg cache read failed, falling back to DB", "err", err)
	}
	if len(msgs) > 0 {
		return msgs, nil
	}

	// Fall back to MongoDB
	docs, err := s.mongo.QueryMessages(ctx, channelID, lastMsgID, limit)
	if err != nil {
		return nil, err
	}

	result := make([]*pb.ImMessage, 0, len(docs))
	for _, doc := range docs {
		result = append(result, &pb.ImMessage{
			MsgId:       doc.MsgID,
			ChannelId:   doc.ChannelID,
			ChannelType: pb.ChannelType(doc.ChannelType),
			Sender: &pb.SenderInfo{
				SenderId:   doc.SenderID,
				SenderName: doc.SenderName,
			},
			Content:  doc.Content,
			MsgType:  pb.MsgType(doc.MsgType),
			MsgParam: doc.MsgParam,
			SendTime: doc.SendTime,
		})
	}
	return result, nil
}

func (s *msgService) AckMsg(ctx context.Context, uid int64, channelID string, msgID int64) error {
	// Clean up offline messages for this user/channel up to msgID.
	// For now this is a no-op; offline cleanup happens on full pull.
	return nil
}

// asyncWriter batches DB writes from the writeCh channel.
func (s *msgService) asyncWriter() {
	batch := make([]*store.MessageDoc, 0, 64)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.mongo.InsertMessages(ctx, batch); err != nil {
			s.logger.Error("async write failed", "count", len(batch), "err", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case doc, ok := <-s.writeCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, doc)
			if len(batch) >= 64 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// checkBanCached checks if a user is banned, using local cache first (5s TTL).
func (s *msgService) checkBanCached(ctx context.Context, uid int64) bool {
	cacheKey := fmt.Sprintf("ban:%d", uid)
	if val, ok := s.banCache.Get(cacheKey); ok {
		return val.(bool)
	}
	banned, _, err := s.ban.IsBanned(ctx, uid)
	if err != nil {
		return false // fail open
	}
	s.banCache.Set(cacheKey, banned)
	return banned
}

// checkAvailCached checks if a channel type is available, using local cache (5s TTL).
func (s *msgService) checkAvailCached(ctx context.Context, channelType pb.ChannelType) bool {
	cacheKey := fmt.Sprintf("avail:%d", int32(channelType))
	if val, ok := s.availCache.Get(cacheKey); ok {
		return val.(bool)
	}
	avail, err := s.channels.IsAvailable(ctx, channelType)
	if err != nil {
		return true // fail open
	}
	s.availCache.Set(cacheKey, avail)
	return avail
}
