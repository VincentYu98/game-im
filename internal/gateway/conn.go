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
	pushCh  chan []byte // LOW priority: server-pushed notifications
	reader  *bufio.Reader

	mgr        *ConnManager
	dispatcher *Dispatcher
	logger     *slog.Logger

	lastHeartbeat atomic.Int64 // unix ms
	closedOnce    sync.Once
	done          chan struct{}
}

func newConn(nc net.Conn, mgr *ConnManager, dispatcher *Dispatcher, sendChanSize int, logger *slog.Logger) *Conn {
	connID := uuid.New().String()
	c := &Conn{
		id:         connID,
		netConn:    nc,
		respCh:     make(chan []byte, 64),             // small: responses are rare
		pushCh:     make(chan []byte, sendChanSize),    // large: pushes can burst
		reader:     bufio.NewReaderSize(nc, 4096),
		mgr:        mgr,
		dispatcher: dispatcher,
		logger:     logger.With("connId", connID),
		done:       make(chan struct{}),
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

// Serve starts the read and write loops. Blocks until the connection closes.
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

// writeLoop drains respCh (high priority) before pushCh (low priority).
// Batches multiple pending frames into a single net.Conn.Write call.
func (c *Conn) writeLoop() {
	defer c.Close()

	// Reusable write buffer to batch frames.
	buf := make([]byte, 0, 16384)

	for {
		// Block until at least one frame is available (either channel).
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

		// Then drain pending pushes (up to a cap to avoid starving writes).
		for range 128 {
			select {
			case data := <-c.pushCh:
				buf = append(buf, data...)
			default:
				goto donePush
			}
		}
	donePush:

		// Single write syscall for the entire batch.
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
