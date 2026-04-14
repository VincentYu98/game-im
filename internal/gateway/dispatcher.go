package gateway

import (
	"context"
	"fmt"
	"log/slog"

	pb "game-im/api/pb"
)

// Handler processes a single command frame.
type Handler func(ctx context.Context, conn *Conn, frame *pb.Frame) error

// Dispatcher routes incoming frames to registered handlers by CmdId.
type Dispatcher struct {
	handlers map[uint32]Handler
	logger   *slog.Logger
}

func NewDispatcher(logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		handlers: make(map[uint32]Handler),
		logger:   logger,
	}
}

// Register adds a handler for a command ID.
func (d *Dispatcher) Register(cmdID uint32, h Handler) {
	d.handlers[cmdID] = h
}

// Dispatch looks up the handler for the frame's CmdId and invokes it.
func (d *Dispatcher) Dispatch(ctx context.Context, conn *Conn, frame *pb.Frame) error {
	h, ok := d.handlers[frame.CmdId]
	if !ok {
		d.logger.Warn("unknown command", "cmdId", frame.CmdId, "connId", conn.ID())
		return fmt.Errorf("unknown command: %d", frame.CmdId)
	}
	return h(ctx, conn, frame)
}
