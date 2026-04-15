package app

import (
	"context"
	"log/slog"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
	"game-im/configs"
	"game-im/internal/bus"
	"game-im/internal/delivery"
	"game-im/internal/gateway"
	"game-im/internal/service"
	"game-im/internal/store"
)

// App wires all components and manages the application lifecycle.
type App struct {
	cfg    *configs.Config
	logger *slog.Logger

	redisStore *store.RedisStore
	mongoStore *store.MongoStore
	msgBus     bus.MessageBus

	presenceSvc service.PresenceService
	banSvc      service.BanService
	channelSvc  service.ChannelService
	msgSvc      service.MsgService
	filterChain *service.FilterChain
	engine      *delivery.Engine

	connMgr    *gateway.ConnManager
	dispatcher *gateway.Dispatcher
	heartbeat  *gateway.HeartbeatManager
	gwServer   *gateway.Server
}

// New creates and wires all components.
func New(cfg *configs.Config) (*App, error) {
	// 1. Logger
	logger := setupLogger(cfg.Log)

	// 2. Node ID
	nodeID := cfg.App.NodeID
	if nodeID == "" {
		hostname, _ := os.Hostname()
		nodeID = hostname + "-" + time.Now().Format("150405")
	}
	logger.Info("starting game-im server", "nodeID", nodeID, "env", cfg.App.Env)

	// 3. Redis
	redisStore, err := store.NewRedisStore(cfg.Redis, logger.With("component", "redis"))
	if err != nil {
		return nil, err
	}

	// 4. MongoDB
	mongoStore, err := store.NewMongoStore(cfg.Mongo, logger.With("component", "mongo"))
	if err != nil {
		return nil, err
	}

	// 5. Message Bus
	var msgBus bus.MessageBus
	switch cfg.Bus.Type {
	case "local":
		msgBus = bus.NewLocalBus(logger.With("component", "bus"))
	default:
		msgBus = bus.NewLocalBus(logger.With("component", "bus"))
	}

	// 6. Services
	presenceSvc := service.NewPresenceService(redisStore, logger.With("component", "presence"))
	banSvc := service.NewBanService(redisStore, logger.With("component", "ban"))
	channelSvc := service.NewChannelService(redisStore, mongoStore, logger.With("component", "channel"))

	// 7. Content filter
	dirtyWords := []string{"fuck", "shit", "damn"} // TODO: load from config/file
	filterChain := service.NewFilterChain(
		service.NewDirtyWordFilter(dirtyWords),
	)

	// 8. Gateway ConnManager
	connMgr := gateway.NewConnManager(nodeID, logger.With("component", "connmgr"))

	// 9. Delivery Engine
	offlineQ := delivery.NewOfflineQueue(mongoStore, logger.With("component", "offline"))
	presenceAdapter := service.NewPresenceAdapter(presenceSvc)
	channelAdapter := service.NewChannelAdapter(channelSvc)
	engine := delivery.NewEngine(
		nodeID,
		connMgr, // implements delivery.Pusher
		presenceAdapter,
		channelAdapter,
		msgBus,
		offlineQ,
		logger.With("component", "delivery"),
	)

	// 10. MsgService
	msgSvc := service.NewMsgService(
		redisStore, mongoStore,
		banSvc, channelSvc, filterChain, engine,
		logger.With("component", "msg"),
	)

	// 11. Dispatcher + Handlers
	dispatcher := gateway.NewDispatcher(logger.With("component", "dispatcher"))

	authHandler := gateway.NewAuthHandler(connMgr, presenceSvc, logger.With("component", "auth"))
	msgHandler := gateway.NewMsgHandler(msgSvc)

	dispatcher.Register(gateway.CmdAuthReq, authHandler.Handle)
	dispatcher.Register(gateway.CmdSendMsgReq, msgHandler.HandleSendMsg)
	dispatcher.Register(gateway.CmdPullMsgReq, msgHandler.HandlePullMsg)
	dispatcher.Register(gateway.CmdAckMsgReq, msgHandler.HandleAckMsg)
	dispatcher.Register(gateway.CmdHeartbeatReq, gateway.HandleHeartbeat)

	// 12. Heartbeat Manager
	heartbeat := gateway.NewHeartbeatManager(
		cfg.Gateway.HeartbeatTimeout,
		cfg.Gateway.HeartbeatInterval,
		connMgr,
		logger.With("component", "heartbeat"),
	)

	// 13. Broadcast Ring Buffer — O(1) write, each conn reads via cursor.
	broadcastRing := gateway.NewBroadcastRing(4096)

	// 14. Gateway Server
	gwServer := gateway.NewServer(cfg.Gateway, connMgr, dispatcher, heartbeat, broadcastRing, logger.With("component", "gateway"))

	// 15. Subscribe bus for world/system broadcast.
	// Instead of iterating N connections (O(N)), write to ring buffer O(1).
	// Each conn's ringConsumerLoop will pick it up via its own cursor.
	broadcastNotify := func(_ context.Context, _ string, msg *pb.ImMessage) {
		notify := &pb.WorldNotify{
			ChannelId: msg.ChannelId,
			LatestSeq: msg.MsgId,
		}
		payload, err := proto.Marshal(notify)
		if err != nil {
			return
		}
		data, err := gateway.EncodeRaw(gateway.CmdWorldNotify, 0, payload)
		if err != nil {
			return
		}
		broadcastRing.Put(data) // O(1) — no iteration over connections!
	}
	msgBus.Subscribe(bus.TopicWorldBroadcast, broadcastNotify)
	msgBus.Subscribe(bus.TopicSystemBroadcast, broadcastNotify)

	return &App{
		cfg:         cfg,
		logger:      logger,
		redisStore:  redisStore,
		mongoStore:  mongoStore,
		msgBus:      msgBus,
		presenceSvc: presenceSvc,
		banSvc:      banSvc,
		channelSvc:  channelSvc,
		msgSvc:      msgSvc,
		filterChain: filterChain,
		engine:      engine,
		connMgr:     connMgr,
		dispatcher:  dispatcher,
		heartbeat:   heartbeat,
		gwServer:    gwServer,
	}, nil
}

// Start launches the application.
func (a *App) Start(ctx context.Context) error {
	if err := a.msgBus.Start(ctx); err != nil {
		return err
	}
	return a.gwServer.Start(ctx)
}

// Stop gracefully shuts down all components.
func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("shutting down...")

	// 1. Stop accepting connections
	if err := a.gwServer.Stop(ctx); err != nil {
		a.logger.Error("gateway stop error", "err", err)
	}

	// 2. Stop message bus
	if err := a.msgBus.Stop(ctx); err != nil {
		a.logger.Error("bus stop error", "err", err)
	}

	// 3. Close MongoDB
	if err := a.mongoStore.Close(ctx); err != nil {
		a.logger.Error("mongo close error", "err", err)
	}

	// 4. Close Redis
	if err := a.redisStore.Close(); err != nil {
		a.logger.Error("redis close error", "err", err)
	}

	a.logger.Info("shutdown complete")
	return nil
}

func setupLogger(cfg configs.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}
