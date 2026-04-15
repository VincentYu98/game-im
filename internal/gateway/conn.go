package gateway

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	pb "game-im/api/pb"
)

// Conn represents a single client TCP connection.
type Conn struct {
	id      string
	uid     atomic.Int64 // 0 until authenticated
	netConn net.Conn
	respCh  chan []byte // HIGH priority: request-response frames
	pushCh  chan []byte // LOW priority: per-user pushes (alliance/private)
	reader  *bufio.Reader

	// Ring buffer cursor for world/system broadcast.
	// The conn reads from ring[cursor..writePos) on each wake-up.
	ring   *BroadcastRing
	cursor atomic.Int64

	mgr        *ConnManager
	dispatcher *Dispatcher
	logger     *slog.Logger

	lastHeartbeat atomic.Int64 // unix ms
	closedOnce    sync.Once
	done          chan struct{}
}

func newConn(nc net.Conn, mgr *ConnManager, dispatcher *Dispatcher, sendChanSize int, ring *BroadcastRing, logger *slog.Logger) *Conn {
	connID := uuid.New().String()
	c := &Conn{
		id:         connID,
		netConn:    nc,
		respCh:     make(chan []byte, 64),
		pushCh:     make(chan []byte, sendChanSize),
		reader:     bufio.NewReaderSize(nc, 4096),
		ring:       ring,
		mgr:        mgr,
		dispatcher: dispatcher,
		logger:     logger.With("connId", connID),
		done:       make(chan struct{}),
	}
	// Start cursor at current ring position (only see new messages).
	if ring != nil {
		c.cursor.Store(ring.NewCursor())
	}
	c.lastHeartbeat.Store(time.Now().UnixMilli())
	return c
}

// ID returns the connection's unique identifier.
func (c *Conn) ID() string { return c.id }

// UID returns the authenticated user ID (0 if not yet authenticated).
func (c *Conn) UID() int64 { return c.uid.Load() }

// SetUID binds a user ID to this connection after authentication.
func (c *Conn) SetUID(uid int64) { c.uid.Store(uid) }

// Send enqueues a push message (low priority). Drops if full.
func (c *Conn) Send(data []byte) bool {
	select {
	case c.pushCh <- data:
		return true
	default:
		return false
	}
}

// SendResp enqueues a response frame (high priority). Drops if full.
func (c *Conn) SendResp(data []byte) bool {
	select {
	case c.respCh <- data:
		return true
	default:
		return false
	}
}

// SendFrame marshals and sends a Frame via the push channel.
func (c *Conn) SendFrame(cmdID, seq uint32, payload []byte) error {
	data, err := EncodeRaw(cmdID, seq, payload)
	if err != nil {
		return err
	}
	c.Send(data)
	return nil
}

// Serve starts the read, write, and ring-consumer loops.
func (c *Conn) Serve(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		c.writeLoop()
	}()

	go func() {
		defer wg.Done()
		c.readLoop(ctx)
	}()

	// Ring consumer: wait on ring.cond, feed broadcast data into pushCh.
	// This runs in a 3rd goroutine so writeLoop doesn't need to know about
	// the ring buffer's sync.Cond mechanism.
	if c.ring != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.ringConsumerLoop()
		}()
	}

	wg.Wait()
}

func (c *Conn) readLoop(ctx context.Context) {
	defer c.Close()

	for {
		frame, err := Decode(c.reader)
		if err != nil {
			return
		}
		c.lastHeartbeat.Store(time.Now().UnixMilli())

		if err := c.dispatcher.Dispatch(ctx, c, frame); err != nil {
			c.logger.Warn("dispatch error", "cmdId", frame.CmdId, "err", err)
		}
	}
}

// ringConsumerLoop reads broadcast messages from the ring buffer and
// sends them into pushCh. This decouples the ring buffer's sync.Cond
// from the writeLoop's select{}.
func (c *Conn) ringConsumerLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		cur := c.cursor.Load()
		wp := c.ring.WritePos()

		if cur >= wp {
			// No new data — wait for signal.
			// Use a goroutine-safe wait with done check.
			waitDone := make(chan struct{})
			go func() {
				c.ring.Wait(cur)
				close(waitDone)
			}()
			select {
			case <-waitDone:
				// New data available.
			case <-c.done:
				return
			}
			continue
		}

		// Read all available entries [cur, wp).
		// Skip entries that are too old (overwritten in ring).
		capacity := c.ring.Capacity()
		if wp-cur > capacity {
			// Consumer is too slow — skip to recent.
			cur = wp - capacity
		}

		for pos := cur; pos < wp; pos++ {
			data := c.ring.Get(pos)
			if data == nil {
				continue
			}
			select {
			case c.pushCh <- data:
			default:
				// pushCh full — drop broadcast for this slow consumer.
			}
		}
		c.cursor.Store(wp)
	}
}

// writeLoop drains respCh (high priority), then pushCh (low priority).
// Batches multiple pending frames into a single net.Conn.Write call.
func (c *Conn) writeLoop() {
	defer c.Close()
	buf := make([]byte, 0, 16384)

	for {
		// Block until at least one frame is available.
		select {
		case data, ok := <-c.respCh:
			if !ok {
				return
			}
			buf = append(buf[:0], data...)
		case data, ok := <-c.pushCh:
			if !ok {
				return
			}
			buf = append(buf[:0], data...)
		case <-c.done:
			c.drainAndClose()
			return
		}

		// Drain all pending responses first (high priority batch).
		for {
			select {
			case data := <-c.respCh:
				buf = append(buf, data...)
			default:
				goto doneResp
			}
		}
	doneResp:

		// Then drain pending pushes (up to cap).
		for range 256 {
			select {
			case data := <-c.pushCh:
				buf = append(buf, data...)
			default:
				goto donePush
			}
		}
	donePush:

		if _, err := c.netConn.Write(buf); err != nil {
			return
		}
	}
}

func (c *Conn) drainAndClose() {
	for {
		select {
		case data := <-c.respCh:
			c.netConn.Write(data)
		case data := <-c.pushCh:
			c.netConn.Write(data)
		default:
			return
		}
	}
}

// Close shuts down the connection (idempotent).
func (c *Conn) Close() {
	c.closedOnce.Do(func() {
		close(c.done)
		c.netConn.Close()
		c.mgr.Unregister(c)
	})
}

// LastHeartbeat returns the time of the last received heartbeat in unix ms.
func (c *Conn) LastHeartbeat() int64 {
	return c.lastHeartbeat.Load()
}

// Reply sends a high-priority response frame for a given request.
func (c *Conn) Reply(frame *pb.Frame, respCmdID uint32, payload []byte) error {
	data, err := EncodeRaw(respCmdID, frame.Seq, payload)
	if err != nil {
		return err
	}
	c.SendResp(data)
	return nil
}
