package framing

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	body := []byte(`{"type":"PING"}`)
	got, err := Decode(Encode(body))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got %q, want %q", got, body)
	}
}

func TestDecodeRejectsMismatchedLength(t *testing.T) {
	frame := Encode([]byte("hello"))
	if _, err := Decode(frame[:len(frame)-1]); err == nil {
		t.Error("expected error for truncated datagram")
	}
	if _, err := Decode(append(frame, 'x')); err == nil {
		t.Error("expected error for datagram with trailing bytes")
	}
	if _, err := Decode([]byte{0, 0}); err == nil {
		t.Error("expected error for datagram shorter than the prefix")
	}
}

// On a stream, frames written back to back must come out one at a time,
// which is the whole reason framing exists for TCP.
func TestStreamKeepsFramesApart(t *testing.T) {
	var stream bytes.Buffer
	frames := [][]byte{[]byte("first"), {}, bytes.Repeat([]byte{0xAB}, 1000)}
	for _, f := range frames {
		if err := Write(&stream, f); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	for i, want := range frames {
		got, err := Read(&stream, 4096)
		if err != nil {
			t.Fatalf("Read frame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d: got %d bytes, want %d", i, len(got), len(want))
		}
	}
	if _, err := Read(&stream, 4096); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF at end of stream, got %v", err)
	}
}

func TestReadRejectsOversizeFrame(t *testing.T) {
	var stream bytes.Buffer
	Write(&stream, make([]byte, 100))
	if _, err := Read(&stream, 99); err == nil {
		t.Fatal("expected error for frame larger than maxSize")
	}
}

func TestReadDetectsTruncatedBody(t *testing.T) {
	frame := Encode([]byte("hello"))
	if _, err := Read(bytes.NewReader(frame[:6]), 100); err == nil {
		t.Fatal("expected error when the stream ends mid-frame")
	}
}
