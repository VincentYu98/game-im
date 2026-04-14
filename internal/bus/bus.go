package bus

import (
	"context"

	pb "game-im/api/pb"
)

// Topic constants for the IM message bus.
const (
	TopicWorldBroadcast  = "im.broadcast.world"
	TopicSystemBroadcast = "im.broadcast.system"
	TopicOfflinePush     = "im.offline.push"
	TopicAudit           = "im.audit"
)

// Handler processes a message from a subscribed topic.
type Handler func(ctx context.Context, topic string, msg *pb.ImMessage)

// MessageBus is the pub/sub abstraction.
// Current: in-process channels (LocalBus).
// Future: Kafka, RabbitMQ, Redis Pub/Sub.
type MessageBus interface {
	Publish(ctx context.Context, topic string, msg *pb.ImMessage) error
	Subscribe(topic string, handler Handler) (unsubscribe func(), err error)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
