package delivery

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
	"game-im/internal/bus"
)

// Pusher pushes raw bytes to a connected user by UID.
// Implemented by gateway.ConnManager.
type Pusher interface {
	PushToUID(uid int64, data []byte) error
}

// PresenceInfo holds online status for delivery routing.
type PresenceInfo struct {
	UID           int64
	GatewayNodeID string
	ConnID        string
	Status        string
}

// PresenceGetter retrieves online presence. Satisfied by service.PresenceService.
type PresenceGetter interface {
	BatchGetPresence(ctx context.Context, uids []int64) (map[int64]*PresenceInfo, error)
}

// MemberGetter retrieves channel members. Satisfied by service.ChannelService.
type MemberGetter interface {
	GetMembers(ctx context.Context, channelID string) ([]int64, error)
}

// OfflineEnqueuer stores messages for offline users.
type OfflineEnqueuer interface {
	Enqueue(ctx context.Context, uid int64, msg *pb.ImMessage) error
}

// Engine routes messages to online/offline recipients.
type Engine struct {
	nodeID   string
	pusher   Pusher
	presence PresenceGetter
	channels MemberGetter
	bus      bus.MessageBus
	offline  OfflineEnqueuer
	logger   *slog.Logger
}

func NewEngine(
	nodeID string,
	pusher Pusher,
	presence PresenceGetter,
	channels MemberGetter,
	msgBus bus.MessageBus,
	offline OfflineEnqueuer,
	logger *slog.Logger,
) *Engine {
	return &Engine{
		nodeID:   nodeID,
		pusher:   pusher,
		presence: presence,
		channels: channels,
		bus:      msgBus,
		offline:  offline,
		logger:   logger,
	}
}

// Deliver routes a message to all recipients based on channel type.
func (e *Engine) Deliver(ctx context.Context, msg *pb.ImMessage) error {
	// World channel: fan-out via message bus to all gateway nodes.
	if msg.ChannelType == pb.ChannelType_CHANNEL_TYPE_WORLD {
		return e.bus.Publish(ctx, bus.TopicWorldBroadcast, msg)
	}

	// System global: fan-out via message bus.
	if msg.ChannelType == pb.ChannelType_CHANNEL_TYPE_SYSTEM {
		return e.bus.Publish(ctx, bus.TopicSystemBroadcast, msg)
	}

	// Other channels: get member list and route individually.
	members, err := e.channels.GetMembers(ctx, msg.ChannelId)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return nil
	}

	presenceMap, err := e.presence.BatchGetPresence(ctx, members)
	if err != nil {
		return err
	}

	pushData, err := marshalPush(msg)
	if err != nil {
		e.logger.Error("failed to marshal push message", "err", err)
		return err
	}

	for _, uid := range members {
		// Don't push to sender.
		if msg.Sender != nil && uid == msg.Sender.SenderId {
			continue
		}

		p := presenceMap[uid]
		if p == nil || p.Status != "online" {
			// Offline: enqueue for later delivery.
			if err := e.offline.Enqueue(ctx, uid, msg); err != nil {
				e.logger.Warn("offline enqueue failed", "uid", uid, "err", err)
			}
			continue
		}

		// In single-binary mode all connections are local.
		// In multi-node mode, check p.GatewayNodeID == e.nodeID;
		// if remote, would RPC to that gateway. For now, always push local.
		if err := e.pusher.PushToUID(uid, pushData); err != nil {
			e.logger.Warn("push to uid failed", "uid", uid, "err", err)
		}
	}

	return nil
}

// marshalPush creates the wire bytes for a PushNewMessage frame.
func marshalPush(msg *pb.ImMessage) ([]byte, error) {
	push := &pb.PushNewMessage{Message: msg}
	payload, err := proto.Marshal(push)
	if err != nil {
		return nil, err
	}

	// Build Frame{CmdId: 4001, Payload: payload}
	frame := &pb.Frame{
		CmdId:   CmdPushNewMessage,
		Payload: payload,
	}
	return proto.Marshal(frame)
}

// Command IDs (duplicated here to avoid circular dependency with gateway).
const (
	CmdPushNewMessage uint32 = 4001
)
