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
	sendCh  chan []byte // buffered outbound queue
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
		sendCh:     make(chan []byte, sendChanSize),
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

// Send enqueues data to the outbound buffer. Drops silently if full (slow consumer).
func (c *Conn) Send(data []byte) bool {
	select {
	case c.sendCh <- data:
		return true
	default:
		c.logger.Warn("send channel full, dropping message", "uid", c.UID())
		return false
	}
}

// SendFrame marshals and sends a Frame to the client.
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
			// Connection closed or read error — normal shutdown path.
			return
		}
		c.lastHeartbeat.Store(time.Now().UnixMilli())

		if err := c.dispatcher.Dispatch(ctx, c, frame); err != nil {
			c.logger.Warn("dispatch error", "cmdId", frame.CmdId, "err", err)
		}
	}
}

func (c *Conn) writeLoop() {
	defer c.Close()

	for {
		select {
		case data, ok := <-c.sendCh:
			if !ok {
				return
			}
			if _, err := c.netConn.Write(data); err != nil {
				return
			}
		case <-c.done:
			// Drain remaining messages before exit.
			for {
				select {
				case data := <-c.sendCh:
					c.netConn.Write(data)
				default:
					return
				}
			}
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

// Reply is a convenience method to send a response for a given Frame.
func (c *Conn) Reply(frame *pb.Frame, respCmdID uint32, payload []byte) error {
	return c.SendFrame(respCmdID, frame.Seq, payload)
}
