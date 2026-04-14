package service

import (
	"context"
	"log/slog"
	"time"

	"game-im/internal/store"
)

type Presence struct {
	UID           int64
	GatewayNodeID string
	ConnID        string
	Status        string // "online" | "offline"
	LastSeen      int64  // unix ms
}

type PresenceService interface {
	SetOnline(ctx context.Context, uid int64, nodeID, connID string) error
	SetOffline(ctx context.Context, uid int64) error
	GetPresence(ctx context.Context, uid int64) (*Presence, error)
	BatchGetPresence(ctx context.Context, uids []int64) (map[int64]*Presence, error)
	RefreshTTL(ctx context.Context, uid int64) error
}

type presenceService struct {
	redis  *store.RedisStore
	logger *slog.Logger
}

func NewPresenceService(redis *store.RedisStore, logger *slog.Logger) PresenceService {
	return &presenceService{redis: redis, logger: logger}
}

func (s *presenceService) SetOnline(ctx context.Context, uid int64, nodeID, connID string) error {
	return s.redis.SetPresence(ctx, uid, &store.PresenceData{
		GatewayNodeID: nodeID,
		ConnID:        connID,
		Status:        "online",
		LastSeen:      time.Now().UnixMilli(),
	})
}

func (s *presenceService) SetOffline(ctx context.Context, uid int64) error {
	return s.redis.DeletePresence(ctx, uid)
}

func (s *presenceService) GetPresence(ctx context.Context, uid int64) (*Presence, error) {
	data, err := s.redis.GetPresence(ctx, uid)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	return &Presence{
		UID:           uid,
		GatewayNodeID: data.GatewayNodeID,
		ConnID:        data.ConnID,
		Status:        data.Status,
		LastSeen:      data.LastSeen,
	}, nil
}

func (s *presenceService) BatchGetPresence(ctx context.Context, uids []int64) (map[int64]*Presence, error) {
	dataMap, err := s.redis.BatchGetPresence(ctx, uids)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]*Presence, len(dataMap))
	for uid, data := range dataMap {
		result[uid] = &Presence{
			UID:           uid,
			GatewayNodeID: data.GatewayNodeID,
			ConnID:        data.ConnID,
			Status:        data.Status,
			LastSeen:      data.LastSeen,
		}
	}
	return result, nil
}

func (s *presenceService) RefreshTTL(ctx context.Context, uid int64) error {
	return s.redis.RefreshPresenceTTL(ctx, uid)
}
