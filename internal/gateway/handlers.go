package gateway

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
	"game-im/internal/service"
	"game-im/pkg/errs"
)

// MsgHandler handles SendMsgReq, PullMsgReq, AckMsgReq.
type MsgHandler struct {
	msgSvc service.MsgService
}

func NewMsgHandler(msgSvc service.MsgService) *MsgHandler {
	return &MsgHandler{msgSvc: msgSvc}
}

func (h *MsgHandler) HandleSendMsg(ctx context.Context, conn *Conn, frame *pb.Frame) error {
	uid := conn.UID()
	if uid == 0 {
		return conn.Reply(frame, CmdSendMsgResp, mustMarshal(&pb.SendMsgResp{
			Code: int32(errs.AuthFailed),
			Msg:  "not authenticated",
		}))
	}

	var req pb.SendMsgReq
	if err := proto.Unmarshal(frame.Payload, &req); err != nil {
		return conn.Reply(frame, CmdSendMsgResp, mustMarshal(&pb.SendMsgResp{
			Code: int32(errs.ServerError),
			Msg:  "invalid request",
		}))
	}

	// Build sender info from connection context.
	// In production, sender info would come from a user profile cache.
	sender := &pb.SenderInfo{
		SenderId: uid,
	}

	resp, err := h.msgSvc.SendMsg(ctx, uid, sender, &service.SendMsgReq{
		ChannelID:   req.ChannelId,
		ChannelType: req.ChannelType,
		Content:     req.Content,
		MsgType:     req.MsgType,
		MsgParam:    req.MsgParam,
		ClientMsgID: req.ClientMsgId,
	})

	if err != nil {
		return conn.Reply(frame, CmdSendMsgResp, mustMarshal(&pb.SendMsgResp{
			Code: int32(errs.ToCode(err)),
			Msg:  errs.ToMsg(err),
		}))
	}

	return conn.Reply(frame, CmdSendMsgResp, mustMarshal(&pb.SendMsgResp{
		Code:     0,
		Msg:      "ok",
		MsgId:    resp.MsgID,
		SendTime: resp.SendTime,
	}))
}

func (h *MsgHandler) HandlePullMsg(ctx context.Context, conn *Conn, frame *pb.Frame) error {
	if conn.UID() == 0 {
		return conn.Reply(frame, CmdPullMsgResp, mustMarshal(&pb.PullMsgResp{
			Code: int32(errs.AuthFailed),
			Msg:  "not authenticated",
		}))
	}

	var req pb.PullMsgReq
	if err := proto.Unmarshal(frame.Payload, &req); err != nil {
		return conn.Reply(frame, CmdPullMsgResp, mustMarshal(&pb.PullMsgResp{
			Code: int32(errs.ServerError),
			Msg:  "invalid request",
		}))
	}

	msgs, err := h.msgSvc.PullMsg(ctx, req.ChannelId, req.LastMsgId, int(req.Limit))
	if err != nil {
		return conn.Reply(frame, CmdPullMsgResp, mustMarshal(&pb.PullMsgResp{
			Code: int32(errs.ToCode(err)),
			Msg:  errs.ToMsg(err),
		}))
	}

	return conn.Reply(frame, CmdPullMsgResp, mustMarshal(&pb.PullMsgResp{
		Code:     0,
		Msg:      "ok",
		Messages: msgs,
	}))
}

func (h *MsgHandler) HandleAckMsg(ctx context.Context, conn *Conn, frame *pb.Frame) error {
	if conn.UID() == 0 {
		return nil
	}

	var req pb.AckMsgReq
	if err := proto.Unmarshal(frame.Payload, &req); err != nil {
		return nil
	}

	return h.msgSvc.AckMsg(ctx, conn.UID(), req.ChannelId, req.MsgId)
}

// HeartbeatHandler handles HeartbeatReq.
func HandleHeartbeat(_ context.Context, conn *Conn, frame *pb.Frame) error {
	resp := &pb.HeartbeatResp{
		ServerTime: time.Now().UnixMilli(),
	}
	return conn.Reply(frame, CmdHeartbeatResp, mustMarshal(resp))
}

func mustMarshal(msg proto.Message) []byte {
	data, _ := proto.Marshal(msg)
	return data
}
