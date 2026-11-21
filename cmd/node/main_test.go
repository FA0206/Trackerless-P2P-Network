package main

import (
	"errors"
	"net"
	"testing"
)

// TestParseAdvertiseIP: only an IPv4 address another machine can dial is
// accepted. Each rejected kind would otherwise fail late and quietly on
// the downloader's side.
func TestParseAdvertiseIP(t *testing.T) {
	tests := []struct {
		in, want string // want "" means rejected
	}{
		{"10.10.0.1", "10.10.0.1"},
		{"192.168.0.101", "192.168.0.101"},
		{"127.0.0.1", "127.0.0.1"},      // right when every node is on this machine
		{"::ffff:10.0.0.1", "10.0.0.1"}, // IPv4-mapped IPv6: canonicalized
		{"laptop-a.local", ""},          // hostname: downloaders drop the record
		{"::1", ""},                     // IPv6: providers are dialed over tcp4
		{"fe80::1", ""},                 // IPv6
		{"0.0.0.0", ""},                 // "this machine" to whoever dials it
		{"", ""},                        // empty
		{"10.10.0.1:9000", ""},          // an address with a port is not an IP
	}
	for _, tt := range tests {
		got, err := parseAdvertiseIP(tt.in)
		if tt.want == "" {
			if err == nil {
				t.Errorf("parseAdvertiseIP(%q) = %q, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("parseAdvertiseIP(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

// TestChooseAdvertiseIP covers every branch of the choice. The route lookup
// is a fake, so no network is involved, and the test checks whether it was
// consulted at all.
func TestChooseAdvertiseIP(t *testing.T) {
	route := func(ip string, err error) func(*net.UDPAddr) (net.IP, error) {
		return func(*net.UDPAddr) (net.IP, error) { return net.ParseIP(ip), err }
	}
	tests := []struct {
		name         string
		value        string
		set          bool
		bootstrap    string // "" = no -bootstrap
		route        func(*net.UDPAddr) (net.IP, error)
		want         string
		wantDetected bool
		wantErr      bool
		wantAsked    bool // was the route lookup consulted?
	}{
		{name: "given explicitly: used as is", value: "10.10.0.7", set: true, bootstrap: "10.10.0.1:9000",
			route: route("10.10.0.2", nil), want: "10.10.0.7"},
		{name: "given explicitly as loopback: still used", value: "127.0.0.1", set: true, bootstrap: "10.10.0.1:9000",
			route: route("10.10.0.2", nil), want: "127.0.0.1"},
		{name: "given explicitly but a hostname: refused", value: "laptop-a.local", set: true,
			route: route("10.10.0.2", nil), wantErr: true},
		{name: "not given, no bootstrap: default", value: "127.0.0.1",
			route: route("10.10.0.2", nil), want: "127.0.0.1"},
		{name: "not given, bootstrap on loopback: default", value: "127.0.0.1", bootstrap: "127.0.0.1:9000",
			route: route("10.10.0.2", nil), want: "127.0.0.1"},
		{name: "not given, bootstrap 0.0.0.0: default", value: "127.0.0.1", bootstrap: "0.0.0.0:9000",
			route: route("10.10.0.2", nil), want: "127.0.0.1"},
		{name: "not given, bootstrap without host: default", value: "127.0.0.1", bootstrap: ":9000",
			route: route("10.10.0.2", nil), want: "127.0.0.1"},
		{name: "not given, bootstrap on another machine: detected", value: "127.0.0.1", bootstrap: "10.10.0.1:9000",
			route: route("10.10.0.2", nil), want: "10.10.0.2", wantDetected: true, wantAsked: true},
		{name: "detection returns loopback: ignored", value: "127.0.0.1", bootstrap: "10.10.0.1:9000",
			route: route("127.0.0.1", nil), want: "127.0.0.1", wantAsked: true},
		{name: "detection returns IPv6: ignored", value: "127.0.0.1", bootstrap: "10.10.0.1:9000",
			route: route("fe80::1", nil), want: "127.0.0.1", wantAsked: true},
		{name: "detection fails: default", value: "127.0.0.1", bootstrap: "10.10.0.1:9000",
			route: route("", errors.New("network is unreachable")), want: "127.0.0.1", wantAsked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seed *net.UDPAddr
			if tt.bootstrap != "" {
				var err error
				if seed, err = net.ResolveUDPAddr("udp4", tt.bootstrap); err != nil {
					t.Fatalf("resolving %q: %v", tt.bootstrap, err)
				}
			}
			asked := false
			routeSource := func(a *net.UDPAddr) (net.IP, error) {
				asked = true
				return tt.route(a)
			}
			got, detected, err := chooseAdvertiseIP(tt.value, tt.set, seed, routeSource)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want || detected != tt.wantDetected {
				t.Errorf("got (%q, detected=%v), want (%q, detected=%v)", got, detected, tt.want, tt.wantDetected)
			}
			if asked != tt.wantAsked {
				t.Errorf("route lookup consulted = %v, want %v", asked, tt.wantAsked)
			}
		})
	}
}

// TestParseBootstrap: what -bootstrap accepts. Unlike -advertise-ip, a host
// name is fine here: it's resolved once, on this machine.
func TestParseBootstrap(t *testing.T) {
	tests := []struct {
		in, want string // want "" means rejected
	}{
		{"10.10.0.1:9000", "10.10.0.1:9000"},
		{"localhost:9000", "127.0.0.1:9000"}, // a host name, resolved here
		{":9000", ":9000"},                   // no host: this machine
		{"10.10.0.1", ""},                    // the port is missing
		{"10.10.0.1:99999", ""},              // no such port
		{"10.10.0.1:abc", ""},                // not a port number
		{"[::1]:9000", ""},                   // IPv6: nodes are IPv4 only
	}
	for _, tt := range tests {
		got, err := parseBootstrap(tt.in)
		if tt.want == "" {
			if err == nil {
				t.Errorf("parseBootstrap(%q) = %v, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got.String() != tt.want {
			t.Errorf("parseBootstrap(%q) = %v, %v; want %s", tt.in, got, err, tt.want)
		}
	}
}

// TestRouteSourceIP checks the real route lookup on loopback, where the
// answer is known (and no packet leaves, or is even sent).
func TestRouteSourceIP(t *testing.T) {
	ip, err := routeSourceIP(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	if !ip.IsLoopback() {
		t.Errorf("route source to 127.0.0.1 = %v, want a loopback address", ip)
	}
}
