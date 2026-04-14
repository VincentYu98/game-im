package service

import (
	"context"
	"log/slog"
	"time"

	"game-im/internal/store"
)

type BanService interface {
	Ban(ctx context.Context, uid int64, duration time.Duration, reason string) error
	Unban(ctx context.Context, uid int64) error
	IsBanned(ctx context.Context, uid int64) (banned bool, expireAt int64, err error)
}

type banService struct {
	redis  *store.RedisStore
	logger *slog.Logger
}

func NewBanService(redis *store.RedisStore, logger *slog.Logger) BanService {
	return &banService{redis: redis, logger: logger}
}

func (s *banService) Ban(ctx context.Context, uid int64, duration time.Duration, reason string) error {
	s.logger.Info("user banned", "uid", uid, "duration", duration, "reason", reason)
	return s.redis.SetBan(ctx, uid, duration, reason)
}

func (s *banService) Unban(ctx context.Context, uid int64) error {
	s.logger.Info("user unbanned", "uid", uid)
	return s.redis.DeleteBan(ctx, uid)
}

func (s *banService) IsBanned(ctx context.Context, uid int64) (bool, int64, error) {
	return s.redis.IsBanned(ctx, uid)
}
