package gateway

// Command IDs for the wire protocol.
const (
	CmdAuthReq     uint32 = 1001
	CmdAuthResp    uint32 = 1002
	CmdSendMsgReq  uint32 = 2001
	CmdSendMsgResp uint32 = 2002
	CmdPullMsgReq  uint32 = 2003
	CmdPullMsgResp uint32 = 2004
	CmdAckMsgReq   uint32 = 2005
	CmdHeartbeatReq  uint32 = 3001
	CmdHeartbeatResp uint32 = 3002
	CmdPushNewMessage uint32 = 4001
	CmdWorldNotify    uint32 = 4002
)
