package gateway

import (
	"context"
	"log/slog"
	"time"
)

// HeartbeatManager sweeps connections and closes those that missed heartbeat.
type HeartbeatManager struct {
	timeout  time.Duration
	interval time.Duration
	mgr      *ConnManager
	logger   *slog.Logger
}

func NewHeartbeatManager(timeout, interval time.Duration, mgr *ConnManager, logger *slog.Logger) *HeartbeatManager {
	return &HeartbeatManager{
		timeout:  timeout,
		interval: interval,
		mgr:      mgr,
		logger:   logger,
	}
}

// Run starts the heartbeat sweep loop. Blocks until ctx is cancelled.
func (h *HeartbeatManager) Run(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.sweep()
		}
	}
}

func (h *HeartbeatManager) sweep() {
	now := time.Now().UnixMilli()
	timeoutMs := h.timeout.Milliseconds()

	h.mgr.conns.Range(func(_, val any) bool {
		c := val.(*Conn)
		if now-c.LastHeartbeat() > timeoutMs {
			h.logger.Info("heartbeat timeout, closing connection",
				"connId", c.ID(), "uid", c.UID(),
				"lastHB", time.UnixMilli(c.LastHeartbeat()))
			c.Close()
		}
		return true
	})
}
