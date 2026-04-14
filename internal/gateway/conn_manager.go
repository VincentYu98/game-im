package gateway

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// ConnManager manages all active connections.
type ConnManager struct {
	conns     sync.Map // connID → *Conn
	userConns sync.Map // uid → *Conn
	nodeID    string
	count     atomic.Int64
	logger    *slog.Logger
}

func NewConnManager(nodeID string, logger *slog.Logger) *ConnManager {
	return &ConnManager{
		nodeID: nodeID,
		logger: logger,
	}
}

// Register adds a new connection to the manager.
func (m *ConnManager) Register(c *Conn) {
	m.conns.Store(c.ID(), c)
	m.count.Add(1)
	m.logger.Debug("connection registered", "connId", c.ID(), "total", m.count.Load())
}

// Unregister removes a connection from the manager.
func (m *ConnManager) Unregister(c *Conn) {
	m.conns.Delete(c.ID())
	if uid := c.UID(); uid != 0 {
		m.userConns.Delete(uid)
	}
	m.count.Add(-1)
	m.logger.Debug("connection unregistered", "connId", c.ID(), "uid", c.UID(), "total", m.count.Load())
}

// BindUser associates a uid with this connection.
func (m *ConnManager) BindUser(uid int64, c *Conn) {
	// Kick previous connection for the same uid if exists.
	if prev, loaded := m.userConns.LoadAndDelete(uid); loaded {
		if old := prev.(*Conn); old.ID() != c.ID() {
			m.logger.Info("kicking previous connection", "uid", uid, "oldConn", old.ID())
			old.Close()
		}
	}
	c.SetUID(uid)
	m.userConns.Store(uid, c)
}

// GetByUID returns the connection for a given user, or nil.
func (m *ConnManager) GetByUID(uid int64) *Conn {
	val, ok := m.userConns.Load(uid)
	if !ok {
		return nil
	}
	return val.(*Conn)
}

// PushToUID sends raw bytes to the user's connection.
// Implements delivery.Pusher.
func (m *ConnManager) PushToUID(uid int64, data []byte) error {
	c := m.GetByUID(uid)
	if c == nil {
		return nil // user not connected, silently skip
	}
	c.Send(data)
	return nil
}

// PushToAll sends raw bytes to all authenticated connections.
// Used for world/system broadcast.
func (m *ConnManager) PushToAll(data []byte) {
	m.userConns.Range(func(_, val any) bool {
		c := val.(*Conn)
		c.Send(data)
		return true
	})
}

// Count returns the current number of connections.
func (m *ConnManager) Count() int64 {
	return m.count.Load()
}

// NodeID returns this gateway node's ID.
func (m *ConnManager) NodeID() string {
	return m.nodeID
}
