package kademlia

import (
	"encoding/json"
	"log/slog"
	"net"
	"testing"
	"time"

	"kademlia-demo/internal/framing"
)

// TestListenReportsDatagramSize: the size Listen hands to its handler is the
// full datagram, framing prefix included. The "rpc received" log's bytes
// field (summed for the control-bytes graph)
// relies on it.
func TestListenReportsDatagramSize(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	recv, err := NewTransport(0, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	send, err := NewTransport(0, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer send.Close()

	sizes := make(chan int, 1)
	go recv.Listen(func(_ *net.UDPAddr, _ Envelope, size int) { sizes <- size })

	env := Envelope{Type: MsgPing, RPCID: RandomID(), SenderID: HashID([]byte("sender"))}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	want := len(framing.Encode(body))

	to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: recv.LocalAddr().Port}
	if err := send.Send(to, env); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-sizes:
		if got != want {
			t.Errorf("size = %d, want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no datagram received")
	}
}
