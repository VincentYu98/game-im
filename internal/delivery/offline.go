package delivery

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
	"game-im/internal/store"
)

// OfflineQueue persists messages for offline users.
type OfflineQueue struct {
	mongo  *store.MongoStore
	logger *slog.Logger
}

func NewOfflineQueue(mongo *store.MongoStore, logger *slog.Logger) *OfflineQueue {
	return &OfflineQueue{mongo: mongo, logger: logger}
}

func (q *OfflineQueue) Enqueue(ctx context.Context, uid int64, msg *pb.ImMessage) error {
	// World channel messages are not stored offline (low value, high volume).
	if msg.ChannelType == pb.ChannelType_CHANNEL_TYPE_WORLD {
		return nil
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	doc := &store.OfflineMessageDoc{
		UID:       uid,
		MsgID:     msg.MsgId,
		ChannelID: msg.ChannelId,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
	if err := q.mongo.InsertOfflineMessage(ctx, doc); err != nil {
		q.logger.Error("failed to enqueue offline message",
			"uid", uid, "msgId", msg.MsgId, "err", err)
		return err
	}
	return nil
}
