package kademlia

import (
	"fmt"
	"net"
)

// Contact is everything we need to reach and identify another node:
// its 160-bit ID plus a UDP address. It's the unit stored in k-buckets and
// exchanged in FIND_NODE responses (paper Sec. 2.3: "(IP address, UDP port,
// Node ID) triples").
type Contact struct {
	ID   ID     `json:"id"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// UDPAddr resolves the contact's address for use with net.UDPConn.
func (c Contact) UDPAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP(c.IP), Port: c.Port}
}

func (c Contact) String() string {
	return fmt.Sprintf("%s@%s:%d", c.ID, c.IP, c.Port)
}

// ContactFromUDPAddr builds a Contact from a wire ID and the address a
// packet actually arrived from (rather than trusting a self-reported
// address inside the message body).
func ContactFromUDPAddr(id ID, addr *net.UDPAddr) Contact {
	return Contact{ID: id, IP: addr.IP.String(), Port: addr.Port}
}
