package gateway

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
)

const (
	headerSize   = 4     // 4-byte big-endian length prefix
	maxFrameSize = 65536 // 64KB max frame body
)

// bufPool pools byte slices used by Encode/Decode to reduce GC pressure.
var bufPool = sync.Pool{
	New: func() any { b := make([]byte, 4096); return &b },
}

// Encode writes a length-prefixed Frame to w.
// Wire format: [4-byte big-endian body_len][proto-serialized Frame]
func Encode(w io.Writer, frame *pb.Frame) error {
	body, err := proto.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if len(body) > maxFrameSize {
		return fmt.Errorf("frame too large: %d > %d", len(body), maxFrameSize)
	}

	// Stack-allocated header — no heap escape.
	var header [headerSize]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))

	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// Decode reads exactly one length-prefixed Frame from r (blocking).
func Decode(r io.Reader) (*pb.Frame, error) {
	// Stack-allocated header — no heap escape.
	var header [headerSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}

	bodyLen := binary.BigEndian.Uint32(header[:])
	if bodyLen > maxFrameSize {
		return nil, fmt.Errorf("frame too large: %d > %d", bodyLen, maxFrameSize)
	}
	if bodyLen == 0 {
		return nil, fmt.Errorf("empty frame body")
	}

	// Use pooled buffer if large enough, else allocate.
	var body []byte
	bp := bufPool.Get().(*[]byte)
	if uint32(cap(*bp)) >= bodyLen {
		body = (*bp)[:bodyLen]
	} else {
		b := make([]byte, bodyLen)
		bp = &b
		body = b
	}

	if _, err := io.ReadFull(r, body); err != nil {
		bufPool.Put(bp)
		return nil, fmt.Errorf("read body: %w", err)
	}

	var frame pb.Frame
	if err := proto.Unmarshal(body, &frame); err != nil {
		bufPool.Put(bp)
		return nil, fmt.Errorf("unmarshal frame: %w", err)
	}

	bufPool.Put(bp)
	return &frame, nil
}

// EncodeRaw builds a complete wire packet: header + Frame bytes.
func EncodeRaw(cmdID, seq uint32, payload []byte) ([]byte, error) {
	frame := &pb.Frame{
		CmdId:   cmdID,
		Seq:     seq,
		Payload: payload,
	}
	body, err := proto.Marshal(frame)
	if err != nil {
		return nil, err
	}
	if len(body) > maxFrameSize {
		return nil, fmt.Errorf("frame too large: %d", len(body))
	}

	buf := make([]byte, headerSize+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[headerSize:], body)
	return buf, nil
}
