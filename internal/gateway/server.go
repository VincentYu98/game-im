package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"

	"game-im/configs"
)

// Server is the TCP gateway that accepts client connections.
type Server struct {
	listener   net.Listener
	connMgr    *ConnManager
	dispatcher *Dispatcher
	heartbeat  *HeartbeatManager
	cfg        configs.GatewayConfig
	logger     *slog.Logger
}

func NewServer(
	cfg configs.GatewayConfig,
	connMgr *ConnManager,
	dispatcher *Dispatcher,
	heartbeat *HeartbeatManager,
	logger *slog.Logger,
) *Server {
	return &Server{
		connMgr:    connMgr,
		dispatcher: dispatcher,
		heartbeat:  heartbeat,
		cfg:        cfg,
		logger:     logger,
	}
}

// Start begins listening for TCP connections. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	var err error
	s.listener, err = net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return err
	}
	s.logger.Info("gateway listening", "addr", s.cfg.ListenAddr)

	// Start heartbeat sweep in background.
	go s.heartbeat.Run(ctx)

	// Accept loop.
	for {
		nc, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.logger.Warn("accept error", "err", err)
			continue
		}

		if s.connMgr.Count() >= int64(s.cfg.MaxConnections) {
			s.logger.Warn("max connections reached, rejecting", "remote", nc.RemoteAddr())
			nc.Close()
			continue
		}

		conn := newConn(nc, s.connMgr, s.dispatcher, s.cfg.SendChanSize, s.logger)
		s.connMgr.Register(conn)
		go conn.Serve(ctx)
	}
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(_ context.Context) error {
	if s.listener != nil {
		s.logger.Info("closing gateway listener")
		return s.listener.Close()
	}
	return nil
}
