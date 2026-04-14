package gateway

import (
	"log/slog"
	"net"
	"os"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func newTestConn(t *testing.T) (*Conn, net.Conn) {
	t.Helper()
	logger := testLogger()
	mgr := NewConnManager("test-node", logger)
	dispatcher := NewDispatcher(logger)
	serverSide, clientSide := net.Pipe()
	c := newConn(serverSide, mgr, dispatcher, 16, logger)
	mgr.Register(c)
	return c, clientSide
}

func TestConn_ID(t *testing.T) {
	c, client := newTestConn(t)
	defer client.Close()
	defer c.Close()

	if c.ID() == "" {
		t.Fatal("conn ID should not be empty")
	}
}

func TestConn_UID(t *testing.T) {
	c, client := newTestConn(t)
	defer client.Close()
	defer c.Close()

	if c.UID() != 0 {
		t.Fatal("UID should be 0 before auth")
	}
	c.SetUID(1001)
	if c.UID() != 1001 {
		t.Fatal("UID should be 1001 after SetUID")
	}
}

func TestConn_Send(t *testing.T) {
	c, client := newTestConn(t)
	defer client.Close()
	defer c.Close()

	data := []byte("hello")
	ok := c.Send(data)
	if !ok {
		t.Fatal("Send should return true when channel is not full")
	}
}

func TestConn_Send_DropWhenFull(t *testing.T) {
	logger := testLogger()
	mgr := NewConnManager("test", logger)
	dispatcher := NewDispatcher(logger)
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()

	// sendCh capacity = 2
	c := newConn(serverSide, mgr, dispatcher, 2, logger)
	defer c.Close()

	c.Send([]byte("a"))
	c.Send([]byte("b"))
	// Third send should drop.
	ok := c.Send([]byte("c"))
	if ok {
		t.Fatal("expected Send to return false when channel full")
	}
}

func TestConn_SendFrame_WritesToClient(t *testing.T) {
	c, client := newTestConn(t)
	defer c.Close()

	// Start write loop to drain sendCh.
	go c.writeLoop()

	payload := []byte("test payload")
	if err := c.SendFrame(2002, 1, payload); err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	// Read from client side.
	client.SetReadDeadline(time.Now().Add(1 * time.Second))
	frame, err := Decode(client)
	if err != nil {
		t.Fatalf("client decode: %v", err)
	}
	if frame.CmdId != 2002 || frame.Seq != 1 {
		t.Fatalf("unexpected frame: cmd=%d seq=%d", frame.CmdId, frame.Seq)
	}
}

func TestConn_Close_Idempotent(t *testing.T) {
	c, client := newTestConn(t)
	defer client.Close()

	// Should not panic when called multiple times.
	c.Close()
	c.Close()
	c.Close()
}

func TestConn_LastHeartbeat(t *testing.T) {
	c, client := newTestConn(t)
	defer client.Close()
	defer c.Close()

	hb := c.LastHeartbeat()
	if hb <= 0 {
		t.Fatal("lastHeartbeat should be positive")
	}
	if time.Now().UnixMilli()-hb > 1000 {
		t.Fatal("lastHeartbeat should be recent")
	}
}
