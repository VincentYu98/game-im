package service

import (
	"context"

	"game-im/internal/delivery"
)

// PresenceAdapter wraps PresenceService to satisfy delivery.PresenceGetter.
type PresenceAdapter struct {
	svc PresenceService
}

func NewPresenceAdapter(svc PresenceService) *PresenceAdapter {
	return &PresenceAdapter{svc: svc}
}

func (a *PresenceAdapter) BatchGetPresence(ctx context.Context, uids []int64) (map[int64]*delivery.PresenceInfo, error) {
	presences, err := a.svc.BatchGetPresence(ctx, uids)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]*delivery.PresenceInfo, len(presences))
	for uid, p := range presences {
		result[uid] = &delivery.PresenceInfo{
			UID:           p.UID,
			GatewayNodeID: p.GatewayNodeID,
			ConnID:        p.ConnID,
			Status:        p.Status,
		}
	}
	return result, nil
}

// ChannelAdapter wraps ChannelService to satisfy delivery.MemberGetter.
// ChannelService.GetMembers already has the right signature, but we wrap
// it explicitly for clarity.
type ChannelAdapter struct {
	svc ChannelService
}

func NewChannelAdapter(svc ChannelService) *ChannelAdapter {
	return &ChannelAdapter{svc: svc}
}

func (a *ChannelAdapter) GetMembers(ctx context.Context, channelID string) ([]int64, error) {
	return a.svc.GetMembers(ctx, channelID)
}
