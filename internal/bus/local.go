package bus

import (
	"context"
	"log/slog"
	"sync"

	pb "game-im/api/pb"
)

// LocalBus is an in-process MessageBus backed by direct function calls.
// Suitable for single-binary deployments. Publish dispatches synchronously
// to all subscribers in the caller's goroutine.
type LocalBus struct {
	mu     sync.RWMutex
	subs   map[string][]entry
	nextID int
	logger *slog.Logger
}

type entry struct {
	id      int
	handler Handler
}

func NewLocalBus(logger *slog.Logger) *LocalBus {
	return &LocalBus{
		subs:   make(map[string][]entry),
		logger: logger,
	}
}

func (b *LocalBus) Publish(ctx context.Context, topic string, msg *pb.ImMessage) error {
	b.mu.RLock()
	entries := make([]entry, len(b.subs[topic]))
	copy(entries, b.subs[topic])
	b.mu.RUnlock()

	// Dispatch handlers asynchronously so the sender's goroutine
	// is not blocked by fan-out (e.g. world broadcast to N connections).
	for _, e := range entries {
		go e.handler(ctx, topic, msg)
	}
	return nil
}

func (b *LocalBus) Subscribe(topic string, handler Handler) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	id := b.nextID
	b.subs[topic] = append(b.subs[topic], entry{id: id, handler: handler})

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		entries := b.subs[topic]
		for i, e := range entries {
			if e.id == id {
				b.subs[topic] = append(entries[:i], entries[i+1:]...)
				break
			}
		}
	}
	return unsubscribe, nil
}

func (b *LocalBus) Start(_ context.Context) error {
	b.logger.Info("local message bus started")
	return nil
}

func (b *LocalBus) Stop(_ context.Context) error {
	b.logger.Info("local message bus stopped")
	return nil
}
