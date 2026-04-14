package store

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	pb "game-im/api/pb"
	"game-im/configs"
)

func newTestRedis(t *testing.T) *RedisStore {
	t.Helper()
	cfg := configs.DefaultConfig().Redis
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	s, err := NewRedisStore(cfg, logger)
	if err != nil {
		t.Fatalf("connect redis: %v", err)
	}
	t.Cleanup(func() {
		s.client.FlushDB(context.Background())
		s.Close()
	})
	// Clean before test too.
	s.client.FlushDB(context.Background())
	return s
}

// ─── Presence ───────────────────────────────────────────

func TestPresence_SetGetDelete(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	uid := int64(1001)

	// Initially nil.
	p, err := s.GetPresence(ctx, uid)
	assertNoErr(t, err)
	if p != nil {
		t.Fatal("expected nil presence for new user")
	}

	// Set online.
	assertNoErr(t, s.SetPresence(ctx, uid, &PresenceData{
		GatewayNodeID: "gw-1",
		ConnID:        "conn-abc",
		Status:        "online",
		LastSeen:      time.Now().UnixMilli(),
	}))

	p, err = s.GetPresence(ctx, uid)
	assertNoErr(t, err)
	if p == nil || p.GatewayNodeID != "gw-1" || p.Status != "online" {
		t.Fatalf("unexpected presence: %+v", p)
	}

	// Delete → nil.
	assertNoErr(t, s.DeletePresence(ctx, uid))
	p, err = s.GetPresence(ctx, uid)
	assertNoErr(t, err)
	if p != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestPresence_BatchGet(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()

	for _, uid := range []int64{1, 2, 3} {
		s.SetPresence(ctx, uid, &PresenceData{
			GatewayNodeID: "gw-1",
			ConnID:        "conn",
			Status:        "online",
			LastSeen:      time.Now().UnixMilli(),
		})
	}

	// Query 1,2,3,4 — uid 4 should be absent.
	m, err := s.BatchGetPresence(ctx, []int64{1, 2, 3, 4})
	assertNoErr(t, err)
	if len(m) != 3 {
		t.Fatalf("expected 3 results, got %d", len(m))
	}
	if _, ok := m[4]; ok {
		t.Fatal("uid 4 should not be present")
	}
}

func TestPresence_RefreshTTL(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	uid := int64(99)

	s.SetPresence(ctx, uid, &PresenceData{Status: "online"})
	assertNoErr(t, s.RefreshPresenceTTL(ctx, uid))

	ttl, _ := s.client.TTL(ctx, "im:presence:99").Result()
	if ttl <= 0 {
		t.Fatalf("expected positive TTL, got %v", ttl)
	}
}

// ─── Sequence ───────────────────────────────────────────

func TestSequence_Monotonic(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	ch := "alliance:100"

	seq0, _ := s.GetCurrentSeq(ctx, ch)
	if seq0 != 0 {
		t.Fatalf("expected 0, got %d", seq0)
	}

	var prev int64
	for i := 0; i < 10; i++ {
		seq, err := s.NextSeq(ctx, ch)
		assertNoErr(t, err)
		if seq <= prev {
			t.Fatalf("seq %d not > prev %d", seq, prev)
		}
		prev = seq
	}

	cur, _ := s.GetCurrentSeq(ctx, ch)
	if cur != 10 {
		t.Fatalf("expected 10, got %d", cur)
	}
}

// ─── Dedup ──────────────────────────────────────────────

func TestDedup_CheckAndSet(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	clientID := "msg-uuid-001"

	// First check: miss.
	exists, _, err := s.CheckDedup(ctx, clientID)
	assertNoErr(t, err)
	if exists {
		t.Fatal("expected miss on first check")
	}

	// Set.
	assertNoErr(t, s.SetDedup(ctx, clientID, 42))

	// Second check: hit.
	exists, msgID, err := s.CheckDedup(ctx, clientID)
	assertNoErr(t, err)
	if !exists || msgID != 42 {
		t.Fatalf("expected hit with msgID=42, got exists=%v msgID=%d", exists, msgID)
	}
}

// ─── Channel State ──────────────────────────────────────

func TestChannelState_SetGetDelete(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	chID := "alliance:200"

	cs, _ := s.GetChannelState(ctx, chID)
	if cs != nil {
		t.Fatal("expected nil")
	}

	assertNoErr(t, s.SetChannelState(ctx, &ChannelState{
		ChannelID:   chID,
		ChannelType: 2,
		CreatedAt:   time.Now().UnixMilli(),
	}))

	cs, err := s.GetChannelState(ctx, chID)
	assertNoErr(t, err)
	if cs == nil || cs.ChannelType != 2 {
		t.Fatalf("unexpected: %+v", cs)
	}

	assertNoErr(t, s.DeleteChannelState(ctx, chID))
	cs, _ = s.GetChannelState(ctx, chID)
	if cs != nil {
		t.Fatal("expected nil after delete")
	}
}

// ─── Channel Members ────────────────────────────────────

func TestChannelMembers_AddRemoveGet(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	chID := "alliance:300"

	assertNoErr(t, s.AddChannelMember(ctx, chID, 1))
	assertNoErr(t, s.AddChannelMember(ctx, chID, 2))
	assertNoErr(t, s.AddChannelMember(ctx, chID, 3))

	members, err := s.GetChannelMembers(ctx, chID)
	assertNoErr(t, err)
	if len(members) != 3 {
		t.Fatalf("expected 3, got %d", len(members))
	}

	assertNoErr(t, s.RemoveChannelMember(ctx, chID, 2))
	members, _ = s.GetChannelMembers(ctx, chID)
	if len(members) != 2 {
		t.Fatalf("expected 2 after remove, got %d", len(members))
	}
}

// ─── User Channels ──────────────────────────────────────

func TestUserChannels_AddRemoveGet(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	uid := int64(500)

	assertNoErr(t, s.AddUserChannel(ctx, uid, "alliance:1"))
	assertNoErr(t, s.AddUserChannel(ctx, uid, "private:1:2"))

	chs, err := s.GetUserChannels(ctx, uid)
	assertNoErr(t, err)
	if len(chs) != 2 {
		t.Fatalf("expected 2, got %d", len(chs))
	}

	assertNoErr(t, s.RemoveUserChannel(ctx, uid, "alliance:1"))
	chs, _ = s.GetUserChannels(ctx, uid)
	if len(chs) != 1 {
		t.Fatalf("expected 1, got %d", len(chs))
	}
}

// ─── Message Cache ──────────────────────────────────────

func TestMsgCache_PushAndGet(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	chID := "alliance:400"

	// Push 5 messages.
	for i := int64(1); i <= 5; i++ {
		assertNoErr(t, s.PushMsgCache(ctx, chID, &pb.ImMessage{
			MsgId:     i,
			ChannelId: chID,
			Content:   "hello",
		}))
	}

	// Get all after msgId=0.
	msgs, err := s.GetMsgCache(ctx, chID, 0, 10)
	assertNoErr(t, err)
	if len(msgs) != 5 {
		t.Fatalf("expected 5, got %d", len(msgs))
	}

	// Get after msgId=3 → should get 4,5.
	msgs, _ = s.GetMsgCache(ctx, chID, 3, 10)
	if len(msgs) != 2 {
		t.Fatalf("expected 2, got %d", len(msgs))
	}
	if msgs[0].MsgId != 4 || msgs[1].MsgId != 5 {
		t.Fatalf("expected [4,5], got [%d,%d]", msgs[0].MsgId, msgs[1].MsgId)
	}

	// Limit test.
	msgs, _ = s.GetMsgCache(ctx, chID, 0, 3)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 with limit, got %d", len(msgs))
	}
}

// ─── Ban ────────────────────────────────────────────────

func TestBan_SetCheckDelete(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	uid := int64(600)

	banned, _, err := s.IsBanned(ctx, uid)
	assertNoErr(t, err)
	if banned {
		t.Fatal("should not be banned initially")
	}

	assertNoErr(t, s.SetBan(ctx, uid, 1*time.Hour, "toxic"))

	banned, expireAt, err := s.IsBanned(ctx, uid)
	assertNoErr(t, err)
	if !banned {
		t.Fatal("should be banned")
	}
	if expireAt <= time.Now().UnixMilli() {
		t.Fatal("expireAt should be in the future")
	}

	assertNoErr(t, s.DeleteBan(ctx, uid))
	banned, _, _ = s.IsBanned(ctx, uid)
	if banned {
		t.Fatal("should not be banned after delete")
	}
}

// ─── Rate Limit ─────────────────────────────────────────

func TestRateLimit(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()
	uid := int64(700)

	// First call: allowed.
	ok, err := s.CheckRateLimit(ctx, uid, 2)
	assertNoErr(t, err)
	if !ok {
		t.Fatal("first call should be allowed")
	}

	// Second call: still allowed (limit=2).
	ok, _ = s.CheckRateLimit(ctx, uid, 2)
	if !ok {
		t.Fatal("second call should be allowed")
	}

	// Third call: rejected.
	ok, _ = s.CheckRateLimit(ctx, uid, 2)
	if ok {
		t.Fatal("third call should be rejected (limit=2)")
	}
}

// ─── Channel Availability ───────────────────────────────

func TestChannelAvailability(t *testing.T) {
	s := newTestRedis(t)
	ctx := context.Background()

	// Default: available (key absent).
	ok, err := s.IsChannelAvailable(ctx, 1)
	assertNoErr(t, err)
	if !ok {
		t.Fatal("default should be available")
	}

	// Set unavailable.
	assertNoErr(t, s.SetChannelAvailable(ctx, 1, false))
	ok, _ = s.IsChannelAvailable(ctx, 1)
	if ok {
		t.Fatal("should be unavailable after set")
	}

	// Re-enable.
	assertNoErr(t, s.SetChannelAvailable(ctx, 1, true))
	ok, _ = s.IsChannelAvailable(ctx, 1)
	if !ok {
		t.Fatal("should be available after re-enable")
	}
}

// ─── Helpers ────────────────────────────────────────────

func assertNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
