package test

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
	"game-im/internal/gateway"
)

// TCPClient is a test helper for interacting with the game-im TCP server.
// It buffers server-pushed frames so request/response methods skip over them.
type TCPClient struct {
	conn   net.Conn
	reader *bufio.Reader
	uid    int64

	mu       sync.Mutex
	pushBuf  []*pb.Frame // buffered push frames
}

func NewTCPClient(addr string) (*TCPClient, error) {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return &TCPClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}, nil
}

func (c *TCPClient) Close() {
	c.conn.Close()
}

// Auth sends an AuthReq and returns the AuthResp.
func (c *TCPClient) Auth(uid int64) (*pb.AuthResp, error) {
	req := &pb.AuthReq{
		Token:   fmt.Sprintf("uid:%d", uid),
		Version: "test-1.0",
	}
	payload, _ := proto.Marshal(req)

	frame := &pb.Frame{CmdId: gateway.CmdAuthReq, Seq: 1, Payload: payload}
	if err := gateway.Encode(c.conn, frame); err != nil {
		return nil, err
	}

	respFrame, err := c.readFrameExpect(gateway.CmdAuthResp, 3*time.Second)
	if err != nil {
		return nil, err
	}

	var resp pb.AuthResp
	if err := proto.Unmarshal(respFrame.Payload, &resp); err != nil {
		return nil, err
	}
	c.uid = resp.Uid
	return &resp, nil
}

// SendMsg sends a message and returns the SendMsgResp.
func (c *TCPClient) SendMsg(channelID string, channelType pb.ChannelType, content, clientMsgID string) (*pb.SendMsgResp, error) {
	req := &pb.SendMsgReq{
		ChannelId:   channelID,
		ChannelType: channelType,
		Content:     content,
		MsgType:     pb.MsgType_MSG_TYPE_TEXT,
		ClientMsgId: clientMsgID,
	}
	payload, _ := proto.Marshal(req)

	frame := &pb.Frame{CmdId: gateway.CmdSendMsgReq, Seq: 2, Payload: payload}
	if err := gateway.Encode(c.conn, frame); err != nil {
		return nil, err
	}

	respFrame, err := c.readFrameExpect(gateway.CmdSendMsgResp, 3*time.Second)
	if err != nil {
		return nil, err
	}

	var resp pb.SendMsgResp
	if err := proto.Unmarshal(respFrame.Payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PullMsg sends a PullMsgReq and returns the PullMsgResp.
func (c *TCPClient) PullMsg(channelID string, lastMsgID int64, limit int32) (*pb.PullMsgResp, error) {
	req := &pb.PullMsgReq{
		ChannelId: channelID,
		LastMsgId: lastMsgID,
		Limit:     limit,
	}
	payload, _ := proto.Marshal(req)

	frame := &pb.Frame{CmdId: gateway.CmdPullMsgReq, Seq: 3, Payload: payload}
	if err := gateway.Encode(c.conn, frame); err != nil {
		return nil, err
	}

	respFrame, err := c.readFrameExpect(gateway.CmdPullMsgResp, 3*time.Second)
	if err != nil {
		return nil, err
	}

	var resp pb.PullMsgResp
	if err := proto.Unmarshal(respFrame.Payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Heartbeat sends a HeartbeatReq and returns HeartbeatResp.
func (c *TCPClient) Heartbeat() (*pb.HeartbeatResp, error) {
	req := &pb.HeartbeatReq{Timestamp: time.Now().UnixMilli()}
	payload, _ := proto.Marshal(req)

	frame := &pb.Frame{CmdId: gateway.CmdHeartbeatReq, Seq: 99, Payload: payload}
	if err := gateway.Encode(c.conn, frame); err != nil {
		return nil, err
	}

	respFrame, err := c.readFrameExpect(gateway.CmdHeartbeatResp, 3*time.Second)
	if err != nil {
		return nil, err
	}

	var resp pb.HeartbeatResp
	if err := proto.Unmarshal(respFrame.Payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ReadPush reads the next server-pushed PushNewMessage.
// First checks the internal push buffer, then reads from the wire.
func (c *TCPClient) ReadPush(timeout time.Duration) (*pb.PushNewMessage, error) {
	// Check buffer first.
	c.mu.Lock()
	if len(c.pushBuf) > 0 {
		f := c.pushBuf[0]
		c.pushBuf = c.pushBuf[1:]
		c.mu.Unlock()
		return decodePush(f)
	}
	c.mu.Unlock()

	// Read from wire.
	c.conn.SetReadDeadline(time.Now().Add(timeout))
	f, err := gateway.Decode(c.reader)
	if err != nil {
		return nil, err
	}
	if f.CmdId != gateway.CmdPushNewMessage {
		return nil, fmt.Errorf("expected CmdPushNewMessage (4001), got %d", f.CmdId)
	}
	return decodePush(f)
}

// readFrameExpect reads frames, buffers any interleaved push messages,
// and returns the first frame matching expectedCmd.
func (c *TCPClient) readFrameExpect(expectedCmd uint32, timeout time.Duration) (*pb.Frame, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("timeout waiting for cmd %d", expectedCmd)
		}
		c.conn.SetReadDeadline(time.Now().Add(remaining))
		f, err := gateway.Decode(c.reader)
		if err != nil {
			return nil, err
		}
		if f.CmdId == expectedCmd {
			return f, nil
		}
		// Buffer unexpected frames (pushes).
		c.mu.Lock()
		c.pushBuf = append(c.pushBuf, f)
		c.mu.Unlock()
	}
}

func decodePush(f *pb.Frame) (*pb.PushNewMessage, error) {
	var push pb.PushNewMessage
	if err := proto.Unmarshal(f.Payload, &push); err != nil {
		return nil, err
	}
	return &push, nil
}
