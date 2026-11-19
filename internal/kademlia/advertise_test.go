package kademlia

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"
)

// TestWarnIfAdvertisingLoopback: a node advertising 127.0.0.1 warns once,
// on the first message from another machine (docs/09-multi-device.md, F1).
// The test feeds handleMessage directly with made-up sender addresses. It
// uses PONGs, which are replies: with no RPC waiting for them they're
// dropped, so nothing is sent back to those (fake) addresses.
func TestWarnIfAdvertisingLoopback(t *testing.T) {
	const warning = `msg="advertise ip is loopback but a remote peer contacted us"`
	remote := &net.UDPAddr{IP: net.IPv4(10, 10, 0, 2), Port: 9000}
	local := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9001}

	tests := []struct {
		name      string
		advertise string
		senders   []*net.UDPAddr
		wantWarns int
	}{
		{"loopback, remote peer: warns", "127.0.0.1", []*net.UDPAddr{remote}, 1},
		{"loopback, many remote messages: warns once", "127.0.0.1", []*net.UDPAddr{local, remote, remote, remote}, 1},
		{"loopback, only local peers: silent", "127.0.0.1", []*net.UDPAddr{local, local}, 0},
		{"LAN address, remote peer: silent", "10.10.0.1", []*net.UDPAddr{remote, remote}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer // written only by this goroutine: handleMessage runs synchronously here
			n, err := NewNode(Config{
				ID:          HashID([]byte("advertiser")),
				AdvertiseIP: tt.advertise,
				Logger:      slog.New(slog.NewTextHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer n.Close()

			for i, from := range tt.senders {
				peer := HashID([]byte{byte(i)})
				n.handleMessage(from, Envelope{Type: MsgPong, RPCID: RandomID(), SenderID: peer})
			}
			if got := strings.Count(logs.String(), warning); got != tt.wantWarns {
				t.Errorf("warnings = %d, want %d; logs:\n%s", got, tt.wantWarns, logs.String())
			}
		})
	}
}
