package transfer

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"kademlia-demo/internal/kademlia"
)

// ProviderRecord is the value stored in the DHT under a file's content
// key: "node NodeID has this file; connect to IP:Port over TCP". It's the
// bridge from Kademlia (which finds records) to TCP (which moves data).
//
// It carries its own TCP port because a peer's file server is a separate
// socket from its Kademlia UDP socket. By default the CLI uses the same
// number for both; that's allowed because TCP and UDP have separate port
// spaces.
type ProviderRecord struct {
	NodeID kademlia.ID `json:"node_id"`
	IP     string      `json:"ip"`
	Port   int         `json:"tcp_port"`
}

// Addr is the "ip:port" string to dial.
func (p ProviderRecord) Addr() string {
	return net.JoinHostPort(p.IP, strconv.Itoa(p.Port))
}

func (p ProviderRecord) encode() []byte {
	b, err := json.Marshal(p)
	if err != nil {
		panic("transfer: marshal provider record: " + err.Error()) // plain struct; can't fail
	}
	return b
}

// decodeProviderRecord parses a DHT value. Anyone can STORE anything under
// a key, so values that aren't well-formed records are rejected here.
func decodeProviderRecord(value []byte) (ProviderRecord, error) {
	var p ProviderRecord
	if err := json.Unmarshal(value, &p); err != nil {
		return p, fmt.Errorf("transfer: not a provider record: %w", err)
	}
	if net.ParseIP(p.IP) == nil || p.Port <= 0 || p.Port > 65535 {
		return p, fmt.Errorf("transfer: provider record has bad address %q:%d", p.IP, p.Port)
	}
	return p, nil
}
