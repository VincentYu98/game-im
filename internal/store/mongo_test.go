package store

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"game-im/configs"
)

func newTestMongo(t *testing.T) *MongoStore {
	t.Helper()
	cfg := configs.DefaultConfig().Mongo
	cfg.Database = "game_im_test" // Use separate test database.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	s, err := NewMongoStore(cfg, logger)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	t.Cleanup(func() {
		s.db.Drop(context.Background())
		s.Close(context.Background())
	})
	return s
}

// ─── Messages ───────────────────────────────────────────

func TestMongo_InsertAndQuery(t *testing.T) {
	s := newTestMongo(t)
	ctx := context.Background()

	docs := []*MessageDoc{
		{MsgID: 1, ChannelID: "ch1", ChannelType: 2, SenderID: 100, Content: "hello", MsgType: 1, SendTime: time.Now().UnixMilli(), CreatedAt: time.Now()},
		{MsgID: 2, ChannelID: "ch1", ChannelType: 2, SenderID: 101, Content: "world", MsgType: 1, SendTime: time.Now().UnixMilli(), CreatedAt: time.Now()},
		{MsgID: 3, ChannelID: "ch1", ChannelType: 2, SenderID: 100, Content: "foo", MsgType: 1, SendTime: time.Now().UnixMilli(), CreatedAt: time.Now()},
	}

	for _, d := range docs {
		if err := s.InsertMessage(ctx, d); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Query all.
	result, err := s.QueryMessages(ctx, "ch1", 0, 50)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}

	// Query after msgId=1 → should get 2,3.
	result, err = s.QueryMessages(ctx, "ch1", 1, 50)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	if result[0].MsgID != 2 || result[1].MsgID != 3 {
		t.Fatalf("expected [2,3], got [%d,%d]", result[0].MsgID, result[1].MsgID)
	}

	// Limit.
	result, _ = s.QueryMessages(ctx, "ch1", 0, 2)
	if len(result) != 2 {
		t.Fatalf("expected 2 with limit, got %d", len(result))
	}
}

func TestMongo_InsertDuplicate_Idempotent(t *testing.T) {
	s := newTestMongo(t)
	ctx := context.Background()

	doc := &MessageDoc{MsgID: 1, ChannelID: "ch-dup", ChannelType: 2, SenderID: 100, Content: "a", MsgType: 1, SendTime: 1, CreatedAt: time.Now()}
	if err := s.InsertMessage(ctx, doc); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Duplicate: same (channel_id, msg_id) → no error.
	if err := s.InsertMessage(ctx, doc); err != nil {
		t.Fatalf("duplicate insert should not error: %v", err)
	}

	result, _ := s.QueryMessages(ctx, "ch-dup", 0, 50)
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
}

func TestMongo_InsertMessagesBatch(t *testing.T) {
	s := newTestMongo(t)
	ctx := context.Background()

	docs := make([]*MessageDoc, 100)
	for i := range docs {
		docs[i] = &MessageDoc{
			MsgID:     int64(i + 1),
			ChannelID: "ch-batch",
			ChannelType: 2,
			SenderID:  100,
			Content:   "batch msg",
			MsgType:   1,
			SendTime:  time.Now().UnixMilli(),
			CreatedAt: time.Now(),
		}
	}
	if err := s.InsertMessages(ctx, docs); err != nil {
		t.Fatalf("batch insert: %v", err)
	}

	result, _ := s.QueryMessages(ctx, "ch-batch", 0, 200)
	if len(result) != 100 {
		t.Fatalf("expected 100, got %d", len(result))
	}

	// Re-insert partial overlap (50 existing + 50 new) → no error.
	more := make([]*MessageDoc, 100)
	for i := range more {
		more[i] = &MessageDoc{
			MsgID:     int64(i + 51), // 51..150, overlap with 51..100
			ChannelID: "ch-batch",
			ChannelType: 2,
			SenderID:  100,
			Content:   "more",
			MsgType:   1,
			SendTime:  time.Now().UnixMilli(),
			CreatedAt: time.Now(),
		}
	}
	if err := s.InsertMessages(ctx, more); err != nil {
		t.Fatalf("partial overlap batch: %v", err)
	}

	result, _ = s.QueryMessages(ctx, "ch-batch", 0, 500)
	if len(result) != 150 {
		t.Fatalf("expected 150, got %d", len(result))
	}
}

// ─── Offline Messages ───────────────────────────────────

func TestMongo_Offline_InsertGetDelete(t *testing.T) {
	s := newTestMongo(t)
	ctx := context.Background()
	uid := int64(1001)

	for i := 0; i < 5; i++ {
		if err := s.InsertOfflineMessage(ctx, &OfflineMessageDoc{
			UID:       uid,
			MsgID:     int64(i + 1),
			ChannelID: "ch-off",
			Payload:   []byte("data"),
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("insert offline: %v", err)
		}
	}

	msgs, err := s.GetOfflineMessages(ctx, uid, 10)
	if err != nil {
		t.Fatalf("get offline: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("expected 5, got %d", len(msgs))
	}

	if err := s.DeleteOfflineMessages(ctx, uid); err != nil {
		t.Fatalf("delete offline: %v", err)
	}
	msgs, _ = s.GetOfflineMessages(ctx, uid, 10)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(msgs))
	}
}

// ─── Channels ───────────────────────────────────────────

func TestMongo_Channel_UpsertGetDelete(t *testing.T) {
	s := newTestMongo(t)
	ctx := context.Background()
	chID := "alliance:test"

	// Upsert.
	if err := s.UpsertChannel(ctx, &ChannelDoc{
		ChannelID:   chID,
		ChannelType: 2,
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ch, err := s.GetChannel(ctx, chID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ch == nil || ch.ChannelType != 2 {
		t.Fatalf("unexpected: %+v", ch)
	}

	// Update last active.
	if err := s.UpdateChannelLastActive(ctx, chID); err != nil {
		t.Fatalf("update last active: %v", err)
	}

	// Delete.
	if err := s.DeleteChannel(ctx, chID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ch, _ = s.GetChannel(ctx, chID)
	if ch != nil {
		t.Fatal("expected nil after delete")
	}
}
