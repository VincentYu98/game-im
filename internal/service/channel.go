package service

import (
	"context"
	"log/slog"
	"time"

	pb "game-im/api/pb"
	"game-im/internal/store"
	"game-im/pkg/errs"
)

type Channel struct {
	ChannelID   string
	ChannelType pb.ChannelType
	CreatedAt   int64
	LastActive  int64
}

type ChannelService interface {
	CreateChannel(ctx context.Context, ch *Channel) error
	DeleteChannel(ctx context.Context, channelID string) error
	JoinChannel(ctx context.Context, uid int64, channelID string) error
	LeaveChannel(ctx context.Context, uid int64, channelID string) error
	GetMembers(ctx context.Context, channelID string) ([]int64, error)
	GetUserChannels(ctx context.Context, uid int64) ([]string, error)
	IsAvailable(ctx context.Context, channelType pb.ChannelType) (bool, error)
}

type channelService struct {
	redis  *store.RedisStore
	mongo  *store.MongoStore
	logger *slog.Logger
}

func NewChannelService(redis *store.RedisStore, mongo *store.MongoStore, logger *slog.Logger) ChannelService {
	return &channelService{redis: redis, mongo: mongo, logger: logger}
}

func (s *channelService) CreateChannel(ctx context.Context, ch *Channel) error {
	now := time.Now()

	// Write to Redis
	if err := s.redis.SetChannelState(ctx, &store.ChannelState{
		ChannelID:   ch.ChannelID,
		ChannelType: int32(ch.ChannelType),
		CreatedAt:   now.UnixMilli(),
		LastActive:  now.UnixMilli(),
	}); err != nil {
		return err
	}

	// Write to MongoDB
	if err := s.mongo.UpsertChannel(ctx, &store.ChannelDoc{
		ChannelID:   ch.ChannelID,
		ChannelType: int32(ch.ChannelType),
		CreatedAt:   now,
		LastActive:  now,
	}); err != nil {
		return err
	}

	s.logger.Info("channel created", "channelID", ch.ChannelID, "type", ch.ChannelType)
	return nil
}

func (s *channelService) DeleteChannel(ctx context.Context, channelID string) error {
	if err := s.redis.DeleteChannelState(ctx, channelID); err != nil {
		return err
	}
	if err := s.mongo.DeleteChannel(ctx, channelID); err != nil {
		return err
	}
	s.logger.Info("channel deleted", "channelID", channelID)
	return nil
}

func (s *channelService) JoinChannel(ctx context.Context, uid int64, channelID string) error {
	// Verify channel exists
	state, err := s.redis.GetChannelState(ctx, channelID)
	if err != nil {
		return err
	}
	if state == nil {
		return errs.ErrChannelNotFound
	}

	// Add member to channel
	if err := s.redis.AddChannelMember(ctx, channelID, uid); err != nil {
		return err
	}

	// Add channel to user's index
	if err := s.redis.AddUserChannel(ctx, uid, channelID); err != nil {
		return err
	}

	s.logger.Info("user joined channel", "uid", uid, "channelID", channelID)
	return nil
}

func (s *channelService) LeaveChannel(ctx context.Context, uid int64, channelID string) error {
	if err := s.redis.RemoveChannelMember(ctx, channelID, uid); err != nil {
		return err
	}
	if err := s.redis.RemoveUserChannel(ctx, uid, channelID); err != nil {
		return err
	}

	s.logger.Info("user left channel", "uid", uid, "channelID", channelID)
	return nil
}

func (s *channelService) GetMembers(ctx context.Context, channelID string) ([]int64, error) {
	// World channel has no member list; delivery engine broadcasts to all connections.
	state, err := s.redis.GetChannelState(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if state != nil && pb.ChannelType(state.ChannelType) == pb.ChannelType_CHANNEL_TYPE_WORLD {
		return nil, nil
	}

	return s.redis.GetChannelMembers(ctx, channelID)
}

func (s *channelService) GetUserChannels(ctx context.Context, uid int64) ([]string, error) {
	return s.redis.GetUserChannels(ctx, uid)
}

func (s *channelService) IsAvailable(ctx context.Context, channelType pb.ChannelType) (bool, error) {
	return s.redis.IsChannelAvailable(ctx, int32(channelType))
}
