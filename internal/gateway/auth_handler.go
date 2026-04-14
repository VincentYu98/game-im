package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
	"game-im/internal/service"
	"game-im/pkg/errs"
)

// AuthHandler handles authentication (CmdAuthReq).
type AuthHandler struct {
	connMgr  *ConnManager
	presence service.PresenceService
	logger   *slog.Logger
}

func NewAuthHandler(connMgr *ConnManager, presence service.PresenceService, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{
		connMgr:  connMgr,
		presence: presence,
		logger:   logger,
	}
}

func (h *AuthHandler) Handle(ctx context.Context, conn *Conn, frame *pb.Frame) error {
	var req pb.AuthReq
	if err := proto.Unmarshal(frame.Payload, &req); err != nil {
		return h.sendAuthResp(conn, frame, errs.AuthFailed, "invalid auth request", 0)
	}

	// TODO: Implement real token verification.
	// For now, extract uid from a simple token format: "uid:{uid_value}".
	uid, err := parseSimpleToken(req.Token)
	if err != nil {
		return h.sendAuthResp(conn, frame, errs.AuthFailed, "invalid token", 0)
	}

	// Bind uid to connection
	h.connMgr.BindUser(uid, conn)

	// Write presence to Redis
	if err := h.presence.SetOnline(ctx, uid, h.connMgr.NodeID(), conn.ID()); err != nil {
		h.logger.Error("failed to set presence", "uid", uid, "err", err)
	}

	h.logger.Info("user authenticated", "uid", uid, "connId", conn.ID())
	return h.sendAuthResp(conn, frame, errs.Success, "ok", uid)
}

func (h *AuthHandler) sendAuthResp(conn *Conn, frame *pb.Frame, code errs.Code, msg string, uid int64) error {
	resp := &pb.AuthResp{
		Code:       int32(code),
		Msg:        msg,
		Uid:        uid,
		ServerTime: time.Now().UnixMilli(),
	}
	payload, err := proto.Marshal(resp)
	if err != nil {
		return err
	}
	return conn.Reply(frame, CmdAuthResp, payload)
}

// parseSimpleToken extracts uid from "uid:{value}" format.
// Replace with real JWT/token verification in production.
func parseSimpleToken(token string) (int64, error) {
	var uid int64
	if _, err := fmt.Sscanf(token, "uid:%d", &uid); err != nil {
		return 0, err
	}
	if uid <= 0 {
		return 0, fmt.Errorf("invalid uid: %d", uid)
	}
	return uid, nil
}
