package bus

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "game-im/api/pb"
)

func newTestBus() *LocalBus {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewLocalBus(logger)
}

func TestLocalBus_PublishNoSubscribers(t *testing.T) {
	b := newTestBus()
	err := b.Publish(context.Background(), "test.topic", &pb.ImMessage{MsgId: 1})
	if err != nil {
		t.Fatalf("publish should not error: %v", err)
	}
}

func TestLocalBus_SubscribeAndPublish(t *testing.T) {
	b := newTestBus()
	done := make(chan *pb.ImMessage, 1)

	b.Subscribe("test.topic", func(_ context.Context, _ string, msg *pb.ImMessage) {
		done <- msg
	})

	b.Publish(context.Background(), "test.topic", &pb.ImMessage{MsgId: 42, Content: "hello"})

	select {
	case msg := <-done:
		if msg.MsgId != 42 {
			t.Fatalf("expected msg 42, got %d", msg.MsgId)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestLocalBus_MultipleSubscribers(t *testing.T) {
	b := newTestBus()
	var count atomic.Int32
	var wg sync.WaitGroup

	for range 3 {
		wg.Add(1)
		b.Subscribe("multi", func(_ context.Context, _ string, _ *pb.ImMessage) {
			count.Add(1)
			wg.Done()
		})
	}

	b.Publish(context.Background(), "multi", &pb.ImMessage{})
	wg.Wait()

	if count.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", count.Load())
	}
}

func TestLocalBus_Unsubscribe(t *testing.T) {
	b := newTestBus()
	var count atomic.Int32
	done := make(chan struct{}, 2)

	unsub, _ := b.Subscribe("unsub", func(_ context.Context, _ string, _ *pb.ImMessage) {
		count.Add(1)
		done <- struct{}{}
	})

	b.Publish(context.Background(), "unsub", &pb.ImMessage{})
	<-done
	if count.Load() != 1 {
		t.Fatal("should have received 1 message")
	}

	unsub()
	b.Publish(context.Background(), "unsub", &pb.ImMessage{})
	// Give goroutine time to run if it were to (it shouldn't).
	time.Sleep(50 * time.Millisecond)
	if count.Load() != 1 {
		t.Fatal("should not receive after unsubscribe")
	}
}

func TestLocalBus_DifferentTopics(t *testing.T) {
	b := newTestBus()
	done := make(chan string, 1)

	b.Subscribe("a", func(_ context.Context, topic string, _ *pb.ImMessage) {
		done <- topic
	})

	b.Publish(context.Background(), "b", &pb.ImMessage{})
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("topic 'a' handler should not fire for topic 'b'")
	default:
	}

	b.Publish(context.Background(), "a", &pb.ImMessage{})
	select {
	case got := <-done:
		if got != "a" {
			t.Fatalf("expected 'a', got %q", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout: topic 'a' handler should fire")
	}
}

func TestLocalBus_ConcurrentSafety(t *testing.T) {
	b := newTestBus()
	var count atomic.Int64
	var handlerWg sync.WaitGroup

	b.Subscribe("conc", func(_ context.Context, _ string, _ *pb.ImMessage) {
		count.Add(1)
		handlerWg.Done()
	})

	handlerWg.Add(100)
	var publishWg sync.WaitGroup
	for range 100 {
		publishWg.Add(1)
		go func() {
			defer publishWg.Done()
			b.Publish(context.Background(), "conc", &pb.ImMessage{})
		}()
	}
	publishWg.Wait()
	handlerWg.Wait()

	if count.Load() != 100 {
		t.Fatalf("expected 100, got %d", count.Load())
	}
}
