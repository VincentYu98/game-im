package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
	"game-im/configs"
)

// Redis key format constants.
const (
	keyPresence      = "im:presence:%d"           // uid → Hash
	keySeq           = "im:seq:%s"                // channelID → String (INCR)
	keyDedup         = "im:dedup:%s"              // clientMsgID → String
	keyChannelState  = "im:channel:state:%s"      // channelID → Hash
	keyUserChannels  = "im:user:channels:%d"      // uid → Hash
	keyMsgCache      = "im:msg:cache:%s"          // channelID → List
	keyBan           = "im:ban:%d"                // uid → Hash
	keyRateLimit     = "im:ratelimit:%d"          // uid → String
	keyChannelAvail  = "im:channel:available:%d"  // channelType → String
	keyServerReg     = "im:server:registry"       // Hash
)

const (
	presenceTTL      = 30 * time.Second
	dedupTTL         = 24 * time.Hour
	channelStateTTL  = 7 * 24 * time.Hour
	userChannelsTTL  = 7 * 24 * time.Hour
	msgCacheTTL      = 1 * time.Hour
	msgCacheMaxLen   = 200
	rateLimitTTL     = 1 * time.Second
)

// RedisStore wraps all Redis operations for the IM system.
type RedisStore struct {
	client *redis.Client
	logger *slog.Logger
}

func NewRedisStore(cfg configs.RedisConfig, logger *slog.Logger) (*RedisStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	logger.Info("redis connected", "addr", cfg.Addr)
	return &RedisStore{client: client, logger: logger}, nil
}

func (s *RedisStore) Close() error {
	return s.client.Close()
}

// ─── Presence ───────────────────────────────────────────

type PresenceData struct {
	GatewayNodeID string `json:"gateway_node_id"`
	ConnID        string `json:"conn_id"`
	Status        string `json:"status"`
	LastSeen      int64  `json:"last_seen"`
}

func (s *RedisStore) SetPresence(ctx context.Context, uid int64, data *PresenceData) error {
	key := fmt.Sprintf(keyPresence, uid)
	pipe := s.client.Pipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"gateway_node_id": data.GatewayNodeID,
		"conn_id":         data.ConnID,
		"status":          data.Status,
		"last_seen":       data.LastSeen,
	})
	pipe.Expire(ctx, key, presenceTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *RedisStore) GetPresence(ctx context.Context, uid int64) (*PresenceData, error) {
	key := fmt.Sprintf(keyPresence, uid)
	result, err := s.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, nil // offline
	}
	var lastSeen int64
	fmt.Sscanf(result["last_seen"], "%d", &lastSeen)
	return &PresenceData{
		GatewayNodeID: result["gateway_node_id"],
		ConnID:        result["conn_id"],
		Status:        result["status"],
		LastSeen:      lastSeen,
	}, nil
}

func (s *RedisStore) BatchGetPresence(ctx context.Context, uids []int64) (map[int64]*PresenceData, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	pipe := s.client.Pipeline()
	cmds := make(map[int64]*redis.MapStringStringCmd, len(uids))
	for _, uid := range uids {
		key := fmt.Sprintf(keyPresence, uid)
		cmds[uid] = pipe.HGetAll(ctx, key)
	}
	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, err
	}

	out := make(map[int64]*PresenceData, len(uids))
	for uid, cmd := range cmds {
		result, err := cmd.Result()
		if err != nil || len(result) == 0 {
			continue // offline
		}
		var lastSeen int64
		fmt.Sscanf(result["last_seen"], "%d", &lastSeen)
		out[uid] = &PresenceData{
			GatewayNodeID: result["gateway_node_id"],
			ConnID:        result["conn_id"],
			Status:        result["status"],
			LastSeen:      lastSeen,
		}
	}
	return out, nil
}

func (s *RedisStore) DeletePresence(ctx context.Context, uid int64) error {
	key := fmt.Sprintf(keyPresence, uid)
	return s.client.Del(ctx, key).Err()
}

func (s *RedisStore) RefreshPresenceTTL(ctx context.Context, uid int64) error {
	key := fmt.Sprintf(keyPresence, uid)
	return s.client.Expire(ctx, key, presenceTTL).Err()
}

// ─── Sequence ───────────────────────────────────────────

func (s *RedisStore) NextSeq(ctx context.Context, channelID string) (int64, error) {
	key := fmt.Sprintf(keySeq, channelID)
	return s.client.Incr(ctx, key).Result()
}

// IncrBy atomically increments the seq counter by delta. Used by SeqAllocator
// to reserve a batch of sequence numbers in one call.
func (s *RedisStore) IncrBy(ctx context.Context, channelID string, delta int64) (int64, error) {
	key := fmt.Sprintf(keySeq, channelID)
	return s.client.IncrBy(ctx, key, delta).Result()
}

func (s *RedisStore) GetCurrentSeq(ctx context.Context, channelID string) (int64, error) {
	key := fmt.Sprintf(keySeq, channelID)
	val, err := s.client.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

// ─── Idempotency / Dedup ────────────────────────────────

// CheckDedup returns (exists, cachedMsgID). If the clientMsgID was already seen,
// exists=true and cachedMsgID is the previously assigned msg_id.
func (s *RedisStore) CheckDedup(ctx context.Context, clientMsgID string) (bool, int64, error) {
	key := fmt.Sprintf(keyDedup, clientMsgID)
	val, err := s.client.Get(ctx, key).Int64()
	if err == redis.Nil {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, val, nil
}

// SetDedup marks a clientMsgID as processed with the given msgID.
func (s *RedisStore) SetDedup(ctx context.Context, clientMsgID string, msgID int64) error {
	key := fmt.Sprintf(keyDedup, clientMsgID)
	return s.client.Set(ctx, key, msgID, dedupTTL).Err()
}

// ─── Channel State ──────────────────────────────────────

type ChannelState struct {
	ChannelID   string `json:"channel_id"`
	ChannelType int32  `json:"channel_type"`
	CreatedAt   int64  `json:"created_at"`
	LastActive  int64  `json:"last_active"`
}

func (s *RedisStore) SetChannelState(ctx context.Context, state *ChannelState) error {
	key := fmt.Sprintf(keyChannelState, state.ChannelID)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, key, data, channelStateTTL).Err()
}

func (s *RedisStore) GetChannelState(ctx context.Context, channelID string) (*ChannelState, error) {
	key := fmt.Sprintf(keyChannelState, channelID)
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state ChannelState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *RedisStore) DeleteChannelState(ctx context.Context, channelID string) error {
	key := fmt.Sprintf(keyChannelState, channelID)
	return s.client.Del(ctx, key).Err()
}

func (s *RedisStore) RefreshChannelStateTTL(ctx context.Context, channelID string) error {
	key := fmt.Sprintf(keyChannelState, channelID)
	return s.client.Expire(ctx, key, channelStateTTL).Err()
}

// ─── Channel Members (stored as Set for efficient add/remove) ───

func channelMembersKey(channelID string) string {
	return fmt.Sprintf("im:channel:members:%s", channelID)
}

func (s *RedisStore) AddChannelMember(ctx context.Context, channelID string, uid int64) error {
	return s.client.SAdd(ctx, channelMembersKey(channelID), uid).Err()
}

func (s *RedisStore) RemoveChannelMember(ctx context.Context, channelID string, uid int64) error {
	return s.client.SRem(ctx, channelMembersKey(channelID), uid).Err()
}

func (s *RedisStore) GetChannelMembers(ctx context.Context, channelID string) ([]int64, error) {
	vals, err := s.client.SMembers(ctx, channelMembersKey(channelID)).Result()
	if err != nil {
		return nil, err
	}
	uids := make([]int64, 0, len(vals))
	for _, v := range vals {
		var uid int64
		fmt.Sscanf(v, "%d", &uid)
		uids = append(uids, uid)
	}
	return uids, nil
}

// ─── User Channels Index ────────────────────────────────

func (s *RedisStore) AddUserChannel(ctx context.Context, uid int64, channelID string) error {
	key := fmt.Sprintf(keyUserChannels, uid)
	pipe := s.client.Pipeline()
	pipe.SAdd(ctx, key, channelID)
	pipe.Expire(ctx, key, userChannelsTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *RedisStore) RemoveUserChannel(ctx context.Context, uid int64, channelID string) error {
	key := fmt.Sprintf(keyUserChannels, uid)
	return s.client.SRem(ctx, key, channelID).Err()
}

func (s *RedisStore) GetUserChannels(ctx context.Context, uid int64) ([]string, error) {
	key := fmt.Sprintf(keyUserChannels, uid)
	return s.client.SMembers(ctx, key).Result()
}

// ─── Message Cache ──────────────────────────────────────

func (s *RedisStore) PushMsgCache(ctx context.Context, channelID string, msg *pb.ImMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	key := fmt.Sprintf(keyMsgCache, channelID)
	pipe := s.client.Pipeline()
	pipe.LPush(ctx, key, data)
	pipe.LTrim(ctx, key, 0, msgCacheMaxLen-1)
	pipe.Expire(ctx, key, msgCacheTTL)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisStore) GetMsgCache(ctx context.Context, channelID string, lastMsgID int64, limit int) ([]*pb.ImMessage, error) {
	key := fmt.Sprintf(keyMsgCache, channelID)
	// Read all cached messages and filter by lastMsgID.
	// The list is newest-first (LPUSH), so we reverse.
	data, err := s.client.LRange(ctx, key, 0, int64(msgCacheMaxLen-1)).Result()
	if err != nil {
		return nil, err
	}

	var msgs []*pb.ImMessage
	for i := len(data) - 1; i >= 0; i-- {
		var msg pb.ImMessage
		if err := proto.Unmarshal([]byte(data[i]), &msg); err != nil {
			s.logger.Warn("corrupt msg cache entry", "channelID", channelID, "err", err)
			continue
		}
		if msg.MsgId > lastMsgID {
			msgs = append(msgs, &msg)
		}
	}

	if limit > 0 && len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return msgs, nil
}

// ─── Ban ────────────────────────────────────────────────

func (s *RedisStore) SetBan(ctx context.Context, uid int64, duration time.Duration, reason string) error {
	key := fmt.Sprintf(keyBan, uid)
	expireAt := time.Now().Add(duration).UnixMilli()
	pipe := s.client.Pipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"reason":    reason,
		"expire_at": expireAt,
	})
	pipe.Expire(ctx, key, duration)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *RedisStore) DeleteBan(ctx context.Context, uid int64) error {
	key := fmt.Sprintf(keyBan, uid)
	return s.client.Del(ctx, key).Err()
}

func (s *RedisStore) IsBanned(ctx context.Context, uid int64) (bool, int64, error) {
	key := fmt.Sprintf(keyBan, uid)
	result, err := s.client.HGetAll(ctx, key).Result()
	if err != nil {
		return false, 0, err
	}
	if len(result) == 0 {
		return false, 0, nil
	}
	var expireAt int64
	fmt.Sscanf(result["expire_at"], "%d", &expireAt)
	return true, expireAt, nil
}

// ─── Rate Limit ─────────────────────────────────────────

// rateLimitScript atomically increments the counter and sets TTL.
// Returns the counter value after increment. Single round-trip.
var rateLimitScript = redis.NewScript(`
local val = redis.call('INCR', KEYS[1])
if val == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return val
`)

// CheckRateLimit returns true if the user is allowed to send.
// Uses a Lua script for atomic INCR + EXPIRE in a single round-trip.
func (s *RedisStore) CheckRateLimit(ctx context.Context, uid int64, maxPerSecond int64) (bool, error) {
	key := fmt.Sprintf(keyRateLimit, uid)
	val, err := rateLimitScript.Run(ctx, s.client, []string{key}, int(rateLimitTTL.Seconds())).Int64()
	if err != nil {
		return false, err
	}
	return val <= maxPerSecond, nil
}

// ─── Channel Availability ───────────────────────────────

func (s *RedisStore) SetChannelAvailable(ctx context.Context, channelType int32, available bool) error {
	key := fmt.Sprintf(keyChannelAvail, channelType)
	val := "0"
	if available {
		val = "1"
	}
	return s.client.Set(ctx, key, val, 0).Err()
}

func (s *RedisStore) IsChannelAvailable(ctx context.Context, channelType int32) (bool, error) {
	key := fmt.Sprintf(keyChannelAvail, channelType)
	val, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return true, nil // default: available
	}
	if err != nil {
		return false, err
	}
	return val == "1", nil
}

// ─── Lightweight Lua: dedup + rate limit only ───────────

// DedupRateResult holds the result of the slim dedup+rate Lua check.
type DedupRateResult struct {
	DedupExists bool
	DedupMsgID  int64
	RateOK      bool
}

// dedupRateScript checks dedup and rate limit in a single Redis RTT.
// Only 2-3 Redis operations (vs 6 before), runs ~2x faster.
// Returns: [dedup_msg_id or -1, rate_count]
var dedupRateScript = redis.NewScript(`
local dedup_key  = KEYS[1]
local rate_key   = KEYS[2]
local rate_limit = tonumber(ARGV[1])
local rate_ttl   = tonumber(ARGV[2])
local dedup_ttl  = tonumber(ARGV[3])
local seq_hint   = tonumber(ARGV[4])

-- 1. Dedup check
local dedup_val = redis.call('GET', dedup_key)
if dedup_val then
    return {tonumber(dedup_val), 0}
end

-- 2. Rate limit (atomic INCR + EXPIRE)
local rate_count = redis.call('INCR', rate_key)
if rate_count == 1 then
    redis.call('EXPIRE', rate_key, rate_ttl)
end

-- 3. Mark dedup with the seq hint (set by caller from SeqAllocator)
if dedup_ttl > 0 and seq_hint > 0 then
    redis.call('SET', dedup_key, seq_hint, 'EX', dedup_ttl)
end

return {-1, rate_count}
`)

// DedupAndRateCheck performs dedup + rate limit check in 1 Redis RTT.
// Ban and availability are checked from local cache by the caller.
// Seq is allocated by SeqAllocator, passed here as seqHint for dedup marking.
func (s *RedisStore) DedupAndRateCheck(ctx context.Context, clientMsgID string, uid int64, rateLimit int64, seqHint int64) (*DedupRateResult, error) {
	keys := []string{
		fmt.Sprintf(keyDedup, clientMsgID),
		fmt.Sprintf(keyRateLimit, uid),
	}
	dedupTTLSec := 0
	if clientMsgID != "" {
		dedupTTLSec = int(dedupTTL.Seconds())
	}
	args := []any{rateLimit, int(rateLimitTTL.Seconds()), dedupTTLSec, seqHint}

	res, err := dedupRateScript.Run(ctx, s.client, keys, args...).Int64Slice()
	if err != nil {
		return nil, err
	}

	result := &DedupRateResult{
		RateOK: res[1] <= rateLimit,
	}
	if res[0] >= 0 {
		result.DedupExists = true
		result.DedupMsgID = res[0]
	}
	return result, nil
}

// PostSendAsync writes the message cache asynchronously. The caller
// does NOT wait for this — the client already has its response.
// Dedup is already set atomically in the Lua script.
func (s *RedisStore) PostSendAsync(channelID string, msg *pb.ImMessage) {
	go func() {
		ctx := context.Background()
		data, err := proto.Marshal(msg)
		if err != nil {
			s.logger.Warn("post-send marshal failed", "err", err)
			return
		}

		cacheKey := fmt.Sprintf(keyMsgCache, channelID)
		pipe := s.client.Pipeline()
		pipe.LPush(ctx, cacheKey, data)
		pipe.LTrim(ctx, cacheKey, 0, msgCacheMaxLen-1)
		pipe.Expire(ctx, cacheKey, msgCacheTTL)

		if _, err := pipe.Exec(ctx); err != nil {
			s.logger.Warn("post-send pipeline failed", "err", err)
		}
	}()
}
