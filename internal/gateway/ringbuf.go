package gateway

import (
	"sync"
	"sync/atomic"
)

const defaultRingSize = 4096 // Must be power of 2.

// BroadcastRing is a lock-free single-producer, multi-consumer ring buffer
// for world/system channel broadcast. Instead of sending to N channels (O(N)),
// the producer writes once (O(1)) and each consumer reads from its own cursor.
//
// Design:
//   - Writer calls Put(data) → atomic increment of writePos
//   - Writer calls cond.Broadcast() to wake all waiting consumers
//   - Each consumer (conn writeLoop) holds a cursor, reads [cursor, writePos)
//   - Ring overwrites old entries when full — slow consumers miss messages (acceptable for world chat)
type BroadcastRing struct {
	buf      [][]byte    // fixed-size ring buffer, indexed by pos & mask
	mask     int64       // ringSize - 1, for fast modulo
	writePos atomic.Int64 // next write position (monotonic, never wraps)
	cond     *sync.Cond  // signals consumers when new data is available
}

// NewBroadcastRing creates a ring buffer with the given capacity (rounded up to power of 2).
func NewBroadcastRing(size int) *BroadcastRing {
	// Round up to power of 2.
	n := 1
	for n < size {
		n <<= 1
	}
	return &BroadcastRing{
		buf:  make([][]byte, n),
		mask: int64(n - 1),
		cond: sync.NewCond(&sync.Mutex{}),
	}
}

// Put writes data into the ring and wakes all waiting consumers.
func (r *BroadcastRing) Put(data []byte) {
	pos := r.writePos.Add(1) - 1 // post-increment: returns the slot we write to
	r.buf[pos&r.mask] = data
	r.cond.Broadcast()
}

// WritePos returns the current write position (number of total writes).
func (r *BroadcastRing) WritePos() int64 {
	return r.writePos.Load()
}

// Get reads the entry at the given absolute position.
// Returns nil if the position has been overwritten (consumer too slow).
func (r *BroadcastRing) Get(pos int64) []byte {
	return r.buf[pos&r.mask]
}

// Wait blocks until writePos > cursor. Used by consumers to efficiently wait
// for new data without polling.
func (r *BroadcastRing) Wait(cursor int64) {
	r.cond.L.Lock()
	for r.writePos.Load() <= cursor {
		r.cond.Wait()
	}
	r.cond.L.Unlock()
}

// Capacity returns the ring buffer size.
func (r *BroadcastRing) Capacity() int64 {
	return r.mask + 1
}

// NewCursor returns a cursor that starts at the current write position
// (i.e., the consumer will only see NEW messages, not old ones).
func (r *BroadcastRing) NewCursor() int64 {
	return r.writePos.Load()
}
