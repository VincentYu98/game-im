package test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	pb "game-im/api/pb"
	"game-im/configs"
	"game-im/internal/app"
	"game-im/pkg/errs"
)

const testAddr = "127.0.0.1:19000"

var testApp *app.App

// TestMain starts a single server shared by all integration tests.
func TestMain(m *testing.M) {
	cfg := configs.DefaultConfig()
	cfg.Gateway.ListenAddr = testAddr
	cfg.Mongo.Database = "game_im_integration_test"
	cfg.Log.Level = "warn"

	var err error
	testApp, err = app.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "app.New: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go testApp.Start(ctx)
	time.Sleep(300 * time.Millisecond) // wait for listener

	code := m.Run()

	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutCancel()
	testApp.Stop(shutCtx)
	os.Exit(code)
}

// flushRedis clears all keys before each test to avoid cross-test pollution.
func flushRedis(t *testing.T) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()
	rdb.FlushDB(context.Background())
}

func TestIntegration_AuthAndHeartbeat(t *testing.T) {
	flushRedis(t)

	c, err := NewTCPClient(testAddr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// Auth.
	authResp, err := c.Auth(1001)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if authResp.Code != 0 || authResp.Uid != 1001 {
		t.Fatalf("auth failed: %+v", authResp)
	}
	if authResp.ServerTime <= 0 {
		t.Fatal("server_time should be positive")
	}

	// Heartbeat.
	hbResp, err := c.Heartbeat()
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if hbResp.ServerTime <= 0 {
		t.Fatal("heartbeat server_time should be positive")
	}
}

func TestIntegration_SendAndPullMsg(t *testing.T) {
	flushRedis(t)

	c, err := NewTCPClient(testAddr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	c.Auth(2001)

	// Send to world channel.
	sendResp, err := c.SendMsg("world", pb.ChannelType_CHANNEL_TYPE_WORLD, "hello world", "cmsg-001")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sendResp.Code != 0 {
		t.Fatalf("send failed: code=%d msg=%s", sendResp.Code, sendResp.Msg)
	}
	if sendResp.MsgId <= 0 {
		t.Fatal("msg_id should be positive")
	}

	// Wait for async DB write.
	time.Sleep(1 * time.Second)

	// Pull messages.
	pullResp, err := c.PullMsg("world", 0, 50)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pullResp.Code != 0 {
		t.Fatalf("pull failed: %+v", pullResp)
	}
	if len(pullResp.Messages) == 0 {
		t.Fatal("expected at least 1 message")
	}
	if pullResp.Messages[0].Content != "hello world" {
		t.Fatalf("unexpected content: %s", pullResp.Messages[0].Content)
	}
}

func TestIntegration_Idempotency(t *testing.T) {
	flushRedis(t)

	c, err := NewTCPClient(testAddr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	c.Auth(3001)

	// Send same clientMsgId twice.
	resp1, err := c.SendMsg("world", pb.ChannelType_CHANNEL_TYPE_WORLD, "dedup test", "dedup-001")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	resp2, err := c.SendMsg("world", pb.ChannelType_CHANNEL_TYPE_WORLD, "dedup test", "dedup-001")
	if err != nil {
		t.Fatalf("second send: %v", err)
	}

	if resp1.MsgId != resp2.MsgId {
		t.Fatalf("idempotency failed: msgId1=%d msgId2=%d", resp1.MsgId, resp2.MsgId)
	}
}

func TestIntegration_WorldBroadcast(t *testing.T) {
	flushRedis(t)

	// Client A (uid 4001).
	clientA, _ := NewTCPClient(testAddr)
	defer clientA.Close()
	clientA.Auth(4001)

	// Client B (uid 4002).
	clientB, _ := NewTCPClient(testAddr)
	defer clientB.Close()
	clientB.Auth(4002)

	// Client A sends to world channel.
	sendResp, err := clientA.SendMsg("world", pb.ChannelType_CHANNEL_TYPE_WORLD, "broadcast msg", "push-001")
	if err != nil || sendResp.Code != 0 {
		t.Fatalf("send: err=%v resp=%+v", err, sendResp)
	}

	// Client B should receive the push.
	push, err := clientB.ReadPush(3 * time.Second)
	if err != nil {
		t.Fatalf("client B read push: %v", err)
	}
	if push.Message == nil || push.Message.Content != "broadcast msg" {
		t.Fatalf("unexpected push: %+v", push)
	}

	// Client A (sender) should NOT receive the push — sender is skipped in world broadcast.
}

func TestIntegration_RateLimit(t *testing.T) {
	flushRedis(t)

	c, _ := NewTCPClient(testAddr)
	defer c.Close()
	c.Auth(5001)

	// Rate limit is 100 msg/sec. Send 100 messages — all should succeed.
	for i := range 100 {
		resp, _ := c.SendMsg("world", pb.ChannelType_CHANNEL_TYPE_WORLD,
			fmt.Sprintf("msg%d", i), fmt.Sprintf("rl-%d-%d", i, time.Now().UnixNano()))
		if resp.Code != 0 {
			t.Fatalf("msg %d should succeed: code=%d msg=%s", i, resp.Code, resp.Msg)
		}
	}

	// The 101st message should be rate limited.
	resp, _ := c.SendMsg("world", pb.ChannelType_CHANNEL_TYPE_WORLD, "overflow",
		fmt.Sprintf("rl-overflow-%d", time.Now().UnixNano()))
	if resp.Code != int32(errs.RateLimited) {
		t.Fatalf("101st msg should be rate limited, got code=%d msg=%s", resp.Code, resp.Msg)
	}
}

func TestIntegration_DirtyWordFilter(t *testing.T) {
	flushRedis(t)

	c, _ := NewTCPClient(testAddr)
	defer c.Close()
	c.Auth(6001)

	// Send message with dirty word.
	resp, _ := c.SendMsg("world", pb.ChannelType_CHANNEL_TYPE_WORLD, "what the fuck",
		fmt.Sprintf("dirty-%d", time.Now().UnixNano()))
	if resp.Code != 0 {
		t.Fatalf("send should succeed (filter replaces, doesn't reject): code=%d", resp.Code)
	}

	// Wait for async DB write.
	time.Sleep(1 * time.Second)

	// Pull and verify the content was filtered.
	pullResp, _ := c.PullMsg("world", resp.MsgId-1, 1)
	if len(pullResp.Messages) == 0 {
		t.Fatal("expected message")
	}
	msg := pullResp.Messages[0]
	if msg.Content == "what the fuck" {
		t.Fatal("dirty word should have been replaced")
	}
	if msg.Content != "what the ****" {
		t.Fatalf("unexpected filtered content: %q", msg.Content)
	}
}

func TestIntegration_UnauthenticatedSend(t *testing.T) {
	flushRedis(t)

	c, _ := NewTCPClient(testAddr)
	defer c.Close()

	// Send without auth.
	resp, _ := c.SendMsg("world", pb.ChannelType_CHANNEL_TYPE_WORLD, "test", "unauth-001")
	if resp.Code == 0 {
		t.Fatal("unauthenticated send should fail")
	}
}
