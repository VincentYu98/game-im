package gateway

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
)

func TestCodec_RoundTrip(t *testing.T) {
	frame := &pb.Frame{
		CmdId:   2001,
		Seq:     7,
		Payload: []byte("hello world"),
	}

	var buf bytes.Buffer
	if err := Encode(&buf, frame); err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := Decode(&buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.CmdId != frame.CmdId || decoded.Seq != frame.Seq {
		t.Fatalf("mismatch: cmd=%d/%d seq=%d/%d",
			decoded.CmdId, frame.CmdId, decoded.Seq, frame.Seq)
	}
	if !bytes.Equal(decoded.Payload, frame.Payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestCodec_EncodeRaw_RoundTrip(t *testing.T) {
	payload := []byte("test payload")
	data, err := EncodeRaw(3001, 42, payload)
	if err != nil {
		t.Fatalf("encoderaw: %v", err)
	}

	decoded, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.CmdId != 3001 || decoded.Seq != 42 {
		t.Fatalf("cmd=%d seq=%d", decoded.CmdId, decoded.Seq)
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Fatal("payload mismatch")
	}
}

func TestCodec_MultipleFrames(t *testing.T) {
	var buf bytes.Buffer
	for i := uint32(0); i < 5; i++ {
		Encode(&buf, &pb.Frame{CmdId: i, Seq: i, Payload: []byte{byte(i)}})
	}

	for i := uint32(0); i < 5; i++ {
		frame, err := Decode(&buf)
		if err != nil {
			t.Fatalf("decode frame %d: %v", i, err)
		}
		if frame.CmdId != i {
			t.Fatalf("frame %d: cmd=%d", i, frame.CmdId)
		}
	}
}

func TestCodec_FrameTooLarge(t *testing.T) {
	frame := &pb.Frame{
		Payload: make([]byte, maxFrameSize+1),
	}
	var buf bytes.Buffer
	err := Encode(&buf, frame)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

func TestCodec_Decode_BodyTooLarge(t *testing.T) {
	// Craft a header that claims a huge body.
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, maxFrameSize+1)
	_, err := Decode(bytes.NewReader(header))
	if err == nil {
		t.Fatal("expected error for oversized body")
	}
}

func TestCodec_Decode_EmptyBody(t *testing.T) {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, 0)
	_, err := Decode(bytes.NewReader(header))
	if err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestCodec_Decode_PartialHeader(t *testing.T) {
	// Only 2 bytes of header → EOF.
	_, err := Decode(bytes.NewReader([]byte{0x00, 0x01}))
	if err == nil {
		t.Fatal("expected EOF for partial header")
	}
}

func TestCodec_Decode_CorruptBody(t *testing.T) {
	// Valid header but garbage body.
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, 3)
	data := append(header, 0xFF, 0xFF, 0xFF)
	_, err := Decode(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected unmarshal error for corrupt body")
	}
}

func TestCodec_Decode_EOF(t *testing.T) {
	_, err := Decode(bytes.NewReader(nil))
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestCodec_Decode_TruncatedBody(t *testing.T) {
	// Header says 100 bytes, but only 5 bytes follow.
	frame := &pb.Frame{CmdId: 1, Payload: []byte("x")}
	body, _ := proto.Marshal(frame)
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(body)+50)) // claim more than available
	data := append(header, body...)
	_, err := Decode(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error for truncated body")
	}
}
