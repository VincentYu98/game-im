package service

import (
	"context"
	"sync"

	"game-im/internal/store"
)

const seqBatchSize = 1000 // Pre-allocate 1000 seq numbers per batch from Redis.

// SeqAllocator pre-allocates sequence number ranges from Redis and hands
// them out from memory. Reduces Redis INCR calls by ~1000x.
//
// For each channelID, it reserves a range [max-batchSize+1, max] via INCRBY
// and serves from memory until exhausted.
type SeqAllocator struct {
	mu     sync.Mutex
	ranges map[string]*seqRange
	redis  *store.RedisStore
}

type seqRange struct {
	next int64 // next seq to hand out
	max  int64 // upper bound of the reserved range
}

func NewSeqAllocator(redis *store.RedisStore) *SeqAllocator {
	return &SeqAllocator{
		ranges: make(map[string]*seqRange),
		redis:  redis,
	}
}

// Next returns the next monotonically increasing sequence number for the channel.
// Thread-safe. Only calls Redis when the local range is exhausted.
func (a *SeqAllocator) Next(ctx context.Context, channelID string) (int64, error) {
	a.mu.Lock()
	r, ok := a.ranges[channelID]
	if ok && r.next <= r.max {
		seq := r.next
		r.next++
		a.mu.Unlock()
		return seq, nil
	}
	a.mu.Unlock()

	// Refill from Redis: INCRBY reserves a batch atomically.
	newMax, err := a.redis.IncrBy(ctx, channelID, seqBatchSize)
	if err != nil {
		return 0, err
	}
	newStart := newMax - seqBatchSize + 1

	a.mu.Lock()
	a.ranges[channelID] = &seqRange{next: newStart + 1, max: newMax}
	a.mu.Unlock()

	return newStart, nil
}
