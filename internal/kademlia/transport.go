package kademlia

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"kademlia-demo/internal/framing"
)

// maxDatagramSize bounds how much we'll ever read from a single UDP
// packet. Every message type must fit: see maxValueSize/maxValuesPerKey in
// store.go for the largest one.
//
// On localhost (64 KB MTU) nothing is ever split. On a real network
// (Ethernet/Wi-Fi, 1500-byte MTU) one IP packet carries at most 1472 bytes
// of UDP payload; anything bigger travels as IP fragments, and losing any
// one fragment loses the whole message (a failed RPC). Sizes, from the
// JSON wire format: a FIND_NODE_RESP with 20 contacts is about 1.8 KB, and
// first exceeds 1472 bytes at 16 contacts with 192.168.0.1xx addresses (17
// with 10.10.0.x) -- so with k = 20, replies fragment once a node knows
// about 18 or more others. A FIND_VALUE_RESP holding 16 values of 250
// bytes is about 5.6 KB, 4 fragments. Fragments are reassembled fine
// on a LAN; under packet loss they raise rpcs_failed. SIMPLIFICATION:
// measured, not avoided.
const maxDatagramSize = 8192

// Transport owns the raw UDP socket and the wire framing: turning an
// Envelope into bytes on the way out, and bytes back into an Envelope (or
// an error) on the way in. It knows nothing about RPC matching, routing
// tables, or retries -- that's Node's job (node.go).
//
// Framing: each packet is [4-byte big-endian length][JSON body], built by
// the shared internal/framing package that the TCP file transfer also
// uses. A UDP datagram already arrives whole, so here the prefix is a
// sanity check: a datagram that was truncated (e.g. bigger than our read
// buffer) or isn't ours fails the length check and is dropped before we
// try to parse it.
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
	if len(body) > maxDatagramSize-framing.PrefixSize {
		return fmt.Errorf("kademlia: envelope too large (%d bytes)", len(body))
	}

	packet := framing.Encode(body)
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
// size is the datagram's length on the wire (framing prefix included), so
// control traffic can be measured. It's invoked from Listen's read loop
// (see Listen for concurrency notes).
type Handler func(from *net.UDPAddr, env Envelope, size int)

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

		go handler(from, env, n)
	}
}

func decodeEnvelope(packet []byte) (Envelope, error) {
	var env Envelope
	body, err := framing.Decode(packet)
	if err != nil {
		return env, err
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return env, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return env, nil
}
