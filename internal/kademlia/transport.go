package kademlia

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
)

// maxDatagramSize bounds how much we'll ever read from a single UDP
// packet. A FIND_NODE_RESP carrying k=20 contacts JSON-encodes to well
// under 2KB; 8KB leaves generous headroom without risking IP
// fragmentation (the common safe UDP payload ceiling is ~1472 bytes on an
// Ethernet-MTU path, but on localhost -- all this project targets -- much
// larger packets go through unfragmented).
const maxDatagramSize = 8192

// lengthPrefixSize is the size, in bytes, of the length header described
// in docs/03-message-framing.md.
const lengthPrefixSize = 4

// Transport owns the raw UDP socket and the wire framing: turning an
// Envelope into bytes on the way out, and bytes back into an Envelope (or
// an error) on the way in. It knows nothing about RPC matching, routing
// tables, or retries -- that's Node's job (node.go).
//
// Framing: each packet we send is [4-byte big-endian length][JSON body].
// A single UDP datagram is already one atomic unit as far as the kernel
// is concerned -- unlike a TCP stream, recvfrom() returns exactly the
// bytes one sendto() call wrote, never a partial message or two messages
// glued together. So, strictly, a length prefix isn't *required* to find
// message boundaries over UDP the way it would be over TCP. We still add
// one because it gives us a cheap sanity check against truncated/garbled
// packets (if the declared length doesn't match what arrived, something's
// wrong and we drop the packet instead of trying to coerce json.Unmarshal
// into making sense of junk), and because it means the exact same framing
// function could be reused unchanged if this transport were ever swapped
// for TCP (relevant if/when piece transfer is added on a separate TCP
// connection -- see the README's status table). See
// docs/03-message-framing.md for the full reasoning.
type Transport struct {
	conn   *net.UDPConn
	logger *slog.Logger
}

// NewTransport opens a UDP socket bound to the given port on all
// interfaces (0.0.0.0), which is enough for a localhost demo.
func NewTransport(port int, logger *slog.Logger) (*Transport, error) {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: port}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("kademlia: listen udp :%d: %w", port, err)
	}
	return &Transport{conn: conn, logger: logger}, nil
}

// LocalAddr returns the address the socket is bound to.
func (t *Transport) LocalAddr() *net.UDPAddr {
	return t.conn.LocalAddr().(*net.UDPAddr)
}

// Close shuts down the underlying socket.
func (t *Transport) Close() error {
	return t.conn.Close()
}

// Send frames `env` as [length][JSON] and writes it as a single UDP
// datagram to `addr`.
func (t *Transport) Send(addr *net.UDPAddr, env Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("kademlia: marshal envelope: %w", err)
	}
	if len(body) > maxDatagramSize-lengthPrefixSize {
		return fmt.Errorf("kademlia: envelope too large (%d bytes)", len(body))
	}

	packet := make([]byte, lengthPrefixSize+len(body))
	binary.BigEndian.PutUint32(packet, uint32(len(body)))
	copy(packet[lengthPrefixSize:], body)

	n, err := t.conn.WriteToUDP(packet, addr)
	if err != nil {
		return fmt.Errorf("kademlia: write udp to %s: %w", addr, err)
	}
	if n != len(packet) {
		return fmt.Errorf("kademlia: short write to %s: wrote %d of %d bytes", addr, n, len(packet))
	}
	return nil
}

// Handler processes one received envelope from the given source address.
// It's invoked from Listen's read loop (see Listen for concurrency notes).
type Handler func(from *net.UDPAddr, env Envelope)

// Listen reads packets until the socket is closed, parsing and validating
// framing before handing each decoded Envelope to handler. Each handler
// invocation runs in its own goroutine so one slow handler (e.g. one
// waiting on a routing-table lock) can't stall reads of unrelated packets.
// Returns when the socket is closed (net.ErrClosed), which is the normal
// shutdown path, not an error to report.
func (t *Transport) Listen(handler Handler) error {
	buf := make([]byte, maxDatagramSize)
	for {
		n, from, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("kademlia: read udp: %w", err)
		}

		env, err := decodeEnvelope(buf[:n])
		if err != nil {
			t.logger.Warn("dropping malformed packet", "from", from, "error", err)
			continue
		}

		go handler(from, env)
	}
}

func decodeEnvelope(packet []byte) (Envelope, error) {
	var env Envelope
	if len(packet) < lengthPrefixSize {
		return env, fmt.Errorf("packet too short for length prefix: %d bytes", len(packet))
	}
	declaredLen := binary.BigEndian.Uint32(packet[:lengthPrefixSize])
	body := packet[lengthPrefixSize:]
	if int(declaredLen) != len(body) {
		return env, fmt.Errorf("length prefix says %d bytes, packet has %d", declaredLen, len(body))
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return env, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return env, nil
}
