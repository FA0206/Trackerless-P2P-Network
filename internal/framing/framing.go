// Package framing implements the [4-byte big-endian length][body] message
// format used on both of this project's channels: the Kademlia UDP socket
// (one frame per datagram) and the TCP file-transfer connections (many
// frames back to back on one byte stream).
package framing

import (
	"encoding/binary"
	"fmt"
	"io"
)

// PrefixSize is the size of the length header.
const PrefixSize = 4

// Encode returns body with its length prefix, ready to send as a single
// UDP datagram.
func Encode(body []byte) []byte {
	frame := make([]byte, PrefixSize+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[PrefixSize:], body)
	return frame
}

// Decode checks a whole received datagram and returns its body. On UDP
// the kernel already delivers exactly one datagram per read, so the
// prefix is a consistency check here: it must match what actually arrived.
func Decode(packet []byte) ([]byte, error) {
	if len(packet) < PrefixSize {
		return nil, fmt.Errorf("framing: packet too short for length prefix: %d bytes", len(packet))
	}
	declared := binary.BigEndian.Uint32(packet[:PrefixSize])
	body := packet[PrefixSize:]
	if int64(declared) != int64(len(body)) {
		return nil, fmt.Errorf("framing: length prefix says %d bytes, packet has %d", declared, len(body))
	}
	return body, nil
}

// Write sends one frame on a byte stream (e.g. a TCP connection).
func Write(w io.Writer, body []byte) error {
	_, err := w.Write(Encode(body))
	return err
}

// Read reads exactly one frame from a byte stream. On TCP the prefix is
// what makes message boundaries exist at all: a stream can hand us half a
// frame or two frames at once, so we read the 4-byte length first and then
// exactly that many bytes. maxSize caps the length a peer can make us
// allocate.
func Read(r io.Reader, maxSize int) ([]byte, error) {
	var prefix [PrefixSize]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if int64(n) > int64(maxSize) {
		return nil, fmt.Errorf("framing: frame of %d bytes exceeds limit of %d", n, maxSize)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("framing: truncated frame body: %w", err)
	}
	return body, nil
}
