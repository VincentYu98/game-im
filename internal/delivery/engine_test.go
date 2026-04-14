package delivery

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	pb "game-im/api/pb"
	"game-im/internal/bus"
)

// ─── Mocks ──────────────────────────────────────────────

type mockPusher struct {
	mu    sync.Mutex
	calls map[int64][]byte
}

func newMockPusher() *mockPusher {
	return &mockPusher{calls: make(map[int64][]byte)}
}

func (p *mockPusher) PushToUID(uid int64, data []byte) error {
	p.mu.Lock()
	p.calls[uid] = data
	p.mu.Unlock()
	return nil
}

func (p *mockPusher) pushed(uid int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.calls[uid]
	return ok
}

type mockPresence struct {
	data map[int64]*PresenceInfo
}

func (m *mockPresence) BatchGetPresence(_ context.Context, uids []int64) (map[int64]*PresenceInfo, error) {
	result := make(map[int64]*PresenceInfo)
	for _, uid := range uids {
		if p, ok := m.data[uid]; ok {
			result[uid] = p
		}
	}
	return result, nil
}

type mockMembers struct {
	members map[string][]int64
}

func (m *mockMembers) GetMembers(_ context.Context, channelID string) ([]int64, error) {
	return m.members[channelID], nil
}

type mockOffline struct {
	mu       sync.Mutex
	enqueued map[int64]*pb.ImMessage // uid → msg
}

func newMockOffline() *mockOffline {
	return &mockOffline{enqueued: make(map[int64]*pb.ImMessage)}
}

func (m *mockOffline) Enqueue(_ context.Context, uid int64, msg *pb.ImMessage) error {
	m.mu.Lock()
	m.enqueued[uid] = msg
	m.mu.Unlock()
	return nil
}

func (m *mockOffline) has(uid int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.enqueued[uid]
	return ok
}

func testEngine(
	pusher *mockPusher,
	presence *mockPresence,
	members *mockMembers,
) (*Engine, *bus.LocalBus, *mockOffline) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	localBus := bus.NewLocalBus(logger)
	offline := newMockOffline()

	e := NewEngine("node-1", pusher, presence, members, localBus, offline, logger)
	return e, localBus, offline
}

// ─── Tests ──────────────────────────────────────────────

func TestEngine_WorldChannel_PublishesToBus(t *testing.T) {
	pusher := newMockPusher()
	presence := &mockPresence{}
	members := &mockMembers{}
	engine, localBus, _ := testEngine(pusher, presence, members)

	done := make(chan *pb.ImMessage, 1)
	localBus.Subscribe(bus.TopicWorldBroadcast, func(_ context.Context, _ string, msg *pb.ImMessage) {
		done <- msg
	})

	msg := &pb.ImMessage{
		MsgId:       1,
		ChannelId:   "world",
		ChannelType: pb.ChannelType_CHANNEL_TYPE_WORLD,
		Content:     "hello world",
	}

	if err := engine.Deliver(context.Background(), msg); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	select {
	case received := <-done:
		if received.MsgId != 1 {
			t.Fatalf("expected msgId=1, got %d", received.MsgId)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("world message should be published to bus")
	}
}

func TestEngine_SystemChannel_PublishesToBus(t *testing.T) {
	pusher := newMockPusher()
	presence := &mockPresence{}
	members := &mockMembers{}
	engine, localBus, _ := testEngine(pusher, presence, members)

	done := make(chan *pb.ImMessage, 1)
	localBus.Subscribe(bus.TopicSystemBroadcast, func(_ context.Context, _ string, msg *pb.ImMessage) {
		done <- msg
	})

	msg := &pb.ImMessage{
		MsgId:       2,
		ChannelId:   "system:global",
		ChannelType: pb.ChannelType_CHANNEL_TYPE_SYSTEM,
	}
	engine.Deliver(context.Background(), msg)

	select {
	case received := <-done:
		if received.MsgId != 2 {
			t.Fatalf("expected msgId=2, got %d", received.MsgId)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("system message should be published to bus")
	}
}

func TestEngine_AllianceChannel_PushesToOnlineMembers(t *testing.T) {
	pusher := newMockPusher()
	presence := &mockPresence{
		data: map[int64]*PresenceInfo{
			1: {UID: 1, GatewayNodeID: "node-1", Status: "online"},
			2: {UID: 2, GatewayNodeID: "node-1", Status: "online"},
		},
	}
	members := &mockMembers{
		members: map[string][]int64{
			"alliance:1": {1, 2, 3}, // uid 3 is offline (not in presence)
		},
	}
	engine, _, _ := testEngine(pusher, presence, members)

	msg := &pb.ImMessage{
		MsgId:       10,
		ChannelId:   "alliance:1",
		ChannelType: pb.ChannelType_CHANNEL_TYPE_ALLIANCE,
		Sender:      &pb.SenderInfo{SenderId: 1}, // sender = uid 1
	}

	engine.Deliver(context.Background(), msg)

	// uid 1 = sender → should NOT be pushed.
	if pusher.pushed(1) {
		t.Fatal("sender should not receive their own message")
	}
	// uid 2 = online → should be pushed.
	if !pusher.pushed(2) {
		t.Fatal("online member uid=2 should receive push")
	}
	// uid 3 = offline → not pushed (offline enqueue would happen, but mongo is nil so we skip).
}

func TestEngine_EmptyMembers_NoOp(t *testing.T) {
	pusher := newMockPusher()
	presence := &mockPresence{}
	members := &mockMembers{
		members: map[string][]int64{
			"alliance:empty": {},
		},
	}
	engine, _, _ := testEngine(pusher, presence, members)

	msg := &pb.ImMessage{
		ChannelId:   "alliance:empty",
		ChannelType: pb.ChannelType_CHANNEL_TYPE_ALLIANCE,
	}
	if err := engine.Deliver(context.Background(), msg); err != nil {
		t.Fatalf("deliver: %v", err)
	}
}

func TestEngine_NilMemberList_NoOp(t *testing.T) {
	pusher := newMockPusher()
	presence := &mockPresence{}
	members := &mockMembers{
		members: map[string][]int64{}, // channel not found → nil
	}
	engine, _, _ := testEngine(pusher, presence, members)

	msg := &pb.ImMessage{
		ChannelId:   "alliance:unknown",
		ChannelType: pb.ChannelType_CHANNEL_TYPE_ALLIANCE,
	}
	if err := engine.Deliver(context.Background(), msg); err != nil {
		t.Fatalf("deliver: %v", err)
	}
}
