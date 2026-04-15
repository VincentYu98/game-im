package gateway

import (
	"net"
	"testing"
	"time"
)

func makeStubConn(t *testing.T, mgr *ConnManager) (*Conn, net.Conn) {
	t.Helper()
	logger := testLogger()
	dispatcher := NewDispatcher(logger)
	server, client := net.Pipe()
	c := newConn(server, mgr, dispatcher, 16, nil, logger)
	return c, client
}

func TestConnManager_RegisterUnregister(t *testing.T) {
	logger := testLogger()
	mgr := NewConnManager("node-1", logger)

	c, client := makeStubConn(t, mgr)
	defer client.Close()

	mgr.Register(c)
	if mgr.Count() != 1 {
		t.Fatalf("expected 1, got %d", mgr.Count())
	}

	mgr.Unregister(c)
	if mgr.Count() != 0 {
		t.Fatalf("expected 0, got %d", mgr.Count())
	}
}

func TestConnManager_BindUser_GetByUID(t *testing.T) {
	logger := testLogger()
	mgr := NewConnManager("node-1", logger)

	c, client := makeStubConn(t, mgr)
	defer client.Close()
	mgr.Register(c)

	// Before bind.
	if mgr.GetByUID(1001) != nil {
		t.Fatal("should be nil before bind")
	}

	mgr.BindUser(1001, c)
	got := mgr.GetByUID(1001)
	if got == nil || got.ID() != c.ID() {
		t.Fatal("GetByUID should return the bound conn")
	}
}

func TestConnManager_BindUser_KicksOldConn(t *testing.T) {
	logger := testLogger()
	mgr := NewConnManager("node-1", logger)

	c1, client1 := makeStubConn(t, mgr)
	mgr.Register(c1)
	mgr.BindUser(1001, c1)

	c2, client2 := makeStubConn(t, mgr)
	defer client2.Close()
	mgr.Register(c2)
	mgr.BindUser(1001, c2)

	// c1 should be closed (kicked).
	client1.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := client1.Read(buf)
	if err == nil {
		t.Fatal("old connection should be closed after re-bind")
	}

	// New conn should be active.
	got := mgr.GetByUID(1001)
	if got == nil || got.ID() != c2.ID() {
		t.Fatal("GetByUID should return the new conn")
	}
}

func TestConnManager_PushToUID_Unknown(t *testing.T) {
	logger := testLogger()
	mgr := NewConnManager("node-1", logger)

	// Push to unknown UID should not error.
	err := mgr.PushToUID(9999, []byte("data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConnManager_PushToUID(t *testing.T) {
	logger := testLogger()
	mgr := NewConnManager("node-1", logger)

	c, client := makeStubConn(t, mgr)
	defer client.Close()
	mgr.Register(c)
	mgr.BindUser(2001, c)

	// Start write loop.
	go c.writeLoop()

	data, _ := EncodeRaw(4001, 0, []byte("push"))
	mgr.PushToUID(2001, data)

	client.SetReadDeadline(time.Now().Add(1 * time.Second))
	frame, err := Decode(client)
	if err != nil {
		t.Fatalf("client decode: %v", err)
	}
	if frame.CmdId != 4001 {
		t.Fatalf("expected cmd 4001, got %d", frame.CmdId)
	}
}

func TestConnManager_PushToAll(t *testing.T) {
	logger := testLogger()
	mgr := NewConnManager("node-1", logger)

	conns := make([]*Conn, 3)
	clients := make([]net.Conn, 3)
	for i := range 3 {
		c, client := makeStubConn(t, mgr)
		mgr.Register(c)
		mgr.BindUser(int64(i+1), c)
		go c.writeLoop()
		conns[i] = c
		clients[i] = client
	}
	defer func() {
		for _, cl := range clients {
			cl.Close()
		}
	}()

	data, _ := EncodeRaw(4001, 0, []byte("broadcast"))
	mgr.PushToAll(data)

	for i, cl := range clients {
		cl.SetReadDeadline(time.Now().Add(1 * time.Second))
		frame, err := Decode(cl)
		if err != nil {
			t.Fatalf("client %d decode: %v", i, err)
		}
		if frame.CmdId != 4001 {
			t.Fatalf("client %d: expected 4001, got %d", i, frame.CmdId)
		}
	}
}

func TestConnManager_NodeID(t *testing.T) {
	logger := testLogger()
	mgr := NewConnManager("my-node", logger)
	if mgr.NodeID() != "my-node" {
		t.Fatalf("expected 'my-node', got %q", mgr.NodeID())
	}
}
