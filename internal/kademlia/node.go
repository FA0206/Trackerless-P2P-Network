package kademlia

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Default protocol parameters. k and alpha are named exactly as in the
// paper (Sec. 2.2 and 2.3): k is the replication/bucket-size parameter,
// alpha is the lookup concurrency parameter.
const (
	DefaultK          = 20
	DefaultAlpha      = 3
	DefaultRPCTimeout = 2 * time.Second
)

// Node is a running Kademlia participant: its identity, its routing table,
// its UDP transport, and the in-flight RPC bookkeeping needed to match
// replies to requests. This is the type cmd/node wires up and the type
// lookup.go's iterative lookup is a method of.
type Node struct {
	ID          ID
	rt          *RoutingTable
	store       *valueStore
	transport   *Transport
	logger      *slog.Logger
	advertiseIP string
	k           int
	alpha       int
	rpcTimeout  time.Duration

	pendingMu sync.Mutex
	pending   map[ID]chan Envelope // rpc id -> channel the waiting caller reads from

	// advertiseLoopback: advertiseIP is a 127.x address, which only this
	// machine can reach. warnedLoopbackAdvertise makes the warning about it
	// (warnIfAdvertisingLoopback) fire once per node.
	advertiseLoopback       bool
	warnedLoopbackAdvertise atomic.Bool
}

// Config holds everything needed to create a Node. Zero values get
// defaults, so tests can fill in only what they care about.
type Config struct {
	ID   ID
	Port int // UDP port; 0 lets the OS pick a free one
	// AdvertiseIP is the address other nodes should use to reach this one.
	// The DHT itself never needs it: peers record us under the source
	// address of our packets (handleMessage). Only the layers on top
	// announce it, e.g. provider records for file sharing (transfer). Default
	// "127.0.0.1", which only this machine can reach; on several machines
	// it must be this node's IPv4 address on the shared subnet.
	//
	// SIMPLIFICATION: it's configured by the caller (cmd/node takes it
	// from -advertise-ip or detects it from the route to the bootstrap
	// node). There's no discovery of how other nodes see us and no NAT
	// traversal (docs/09-multi-device.md). If it's loopback and a remote
	// peer shows up, warnIfAdvertisingLoopback says so once.
	AdvertiseIP string
	K           int           // default DefaultK
	Alpha       int           // default DefaultAlpha
	RPCTimeout  time.Duration // default DefaultRPCTimeout
	Logger      *slog.Logger  // default NewLogger(ID, Info)
}

// NewNode creates a node listening on cfg.Port. Call Start to begin
// handling packets.
func NewNode(cfg Config) (*Node, error) {
	if cfg.AdvertiseIP == "" {
		cfg.AdvertiseIP = "127.0.0.1"
	}
	if cfg.K == 0 {
		cfg.K = DefaultK
	}
	if cfg.Alpha == 0 {
		cfg.Alpha = DefaultAlpha
	}
	if cfg.RPCTimeout == 0 {
		cfg.RPCTimeout = DefaultRPCTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = NewLogger(cfg.ID, slog.LevelInfo)
	}
	transport, err := NewTransport(cfg.Port, cfg.Logger)
	if err != nil {
		return nil, err
	}
	return &Node{
		ID:          cfg.ID,
		rt:          NewRoutingTable(cfg.ID, cfg.K),
		store:       newValueStore(),
		transport:   transport,
		logger:      cfg.Logger,
		advertiseIP: cfg.AdvertiseIP,
		k:           cfg.K,
		alpha:       cfg.Alpha,
		rpcTimeout:  cfg.RPCTimeout,
		pending:     make(map[ID]chan Envelope),

		advertiseLoopback: isLoopbackIP(cfg.AdvertiseIP),
	}, nil
}

// isLoopbackIP reports whether s is a 127.x (or ::1) address.
func isLoopbackIP(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.IsLoopback()
}

// Addr returns the UDP address this node's socket is bound to.
func (n *Node) Addr() *net.UDPAddr {
	return n.transport.LocalAddr()
}

// Self returns this node's own contact info as other nodes should use it:
// the configured advertise IP plus the bound UDP port.
func (n *Node) Self() Contact {
	return Contact{ID: n.ID, IP: n.advertiseIP, Port: n.Addr().Port}
}

// Logger returns the node's logger, so layers built on top of the DHT
// (e.g. file transfer) log with the same node tag.
func (n *Node) Logger() *slog.Logger {
	return n.logger
}

// Start launches the background read loop that receives and handles
// incoming packets. Call it once, after construction.
func (n *Node) Start() {
	go func() {
		if err := n.transport.Listen(n.handleMessage); err != nil {
			n.logger.Error("transport listen loop exited with error", "error", err)
		}
	}()
}

// Close shuts down the node's socket.
func (n *Node) Close() error {
	return n.transport.Close()
}

// RoutingTableSize returns how many contacts this node currently knows
// about, for logging/demo output.
func (n *Node) RoutingTableSize() int {
	return n.rt.Size()
}

// RoutingTableSnapshot returns every non-empty k-bucket, keyed by bucket
// index (see RoutingTable.Snapshot). Used by the CLI's `table` command.
func (n *Node) RoutingTableSnapshot() map[int][]Contact {
	return n.rt.Snapshot()
}

// handleMessage is the single entry point for every received packet: it
// updates the routing table for whoever sent it (paper Sec. 2.2: "when a
// node receives any message... it updates the appropriate k-bucket for the
// sender's node ID"), then dispatches on message type.
func (n *Node) handleMessage(from *net.UDPAddr, env Envelope, size int) {
	sender := ContactFromUDPAddr(env.SenderID, from)
	n.logger.Info("rpc received", "type", string(env.Type), "from", sender.String(), "rpc_id", shortID(env.RPCID), "bytes", size)
	n.warnIfAdvertisingLoopback(sender)

	n.touchContact(sender)

	switch env.Type {
	case MsgPing:
		n.replyPong(from, env)
	case MsgFindNode:
		n.replyFindNode(from, env)
	case MsgStore:
		n.replyStore(from, env)
	case MsgFindValue:
		n.replyFindValue(from, env)
	case MsgPong, MsgFindNodeResp, MsgStoreResp, MsgFindValueResp:
		n.deliverResponse(env)
	default:
		n.logger.Warn("unknown message type", "type", string(env.Type), "from", sender.String())
	}
}

// warnIfAdvertisingLoopback logs once when a peer on another machine
// contacts us while we advertise a loopback IP. The DHT works regardless
// (that peer records us under our packets' real source address), but the
// layers on top announce AdvertiseIP: our provider records would send that
// peer, and every other remote one, to its own machine, so it can't fetch
// files from us. Checked here, on receipt, so it fires whatever the order
// of events: even a node that shared a file before anyone joined, when
// there was nobody remote to compare with. No I/O (log only).
func (n *Node) warnIfAdvertisingLoopback(sender Contact) {
	if !n.advertiseLoopback || isLoopbackIP(sender.IP) {
		return
	}
	if n.warnedLoopbackAdvertise.CompareAndSwap(false, true) {
		n.logger.Warn("advertise ip is loopback but a remote peer contacted us",
			"advertise_ip", n.advertiseIP, "from", sender.String())
	}
}

// touchContact applies the k-bucket update rule (bucket.go) and, if the
// bucket turns out to be full, performs the paper's ping-then-decide
// eviction check (Sec. 2.2) asynchronously so handling this message never
// blocks on a network round trip to some other contact entirely.
func (n *Node) touchContact(c Contact) {
	outcome, stale := n.rt.Update(c)
	switch outcome {
	case Seen:
		n.logger.Debug("bucket updated", "contact", c.String())
	case FullNeedsPing:
		n.logger.Debug("bucket full, pinging least-recently-seen contact before evicting",
			"stale", stale.String(), "candidate", c.String())
		go n.resolveEviction(stale, c)
	}
}

func (n *Node) resolveEviction(stale, candidate Contact) {
	ctx, cancel := context.WithTimeout(context.Background(), n.rpcTimeout)
	defer cancel()
	if n.ping(ctx, stale) {
		n.rt.KeepAlive(stale)
		n.logger.Debug("stale contact still alive, kept; new contact discarded",
			"kept", stale.String(), "discarded", candidate.String())
	} else {
		n.rt.ReplaceStale(stale, candidate)
		n.logger.Debug("stale contact unresponsive, evicted",
			"evicted", stale.String(), "replacement", candidate.String())
	}
}

// sendAndWait sends a request and blocks until a reply with a matching RPC
// ID arrives or ctx is done. It's the shared plumbing behind Ping and
// FindNode.
func (n *Node) sendAndWait(ctx context.Context, target Contact, msgType MessageType, payload any) (Envelope, error) {
	rpcID := RandomID()
	respCh := make(chan Envelope, 1)

	n.pendingMu.Lock()
	n.pending[rpcID] = respCh
	n.pendingMu.Unlock()
	defer func() {
		n.pendingMu.Lock()
		delete(n.pending, rpcID)
		n.pendingMu.Unlock()
	}()

	env := Envelope{Type: msgType, RPCID: rpcID, SenderID: n.ID}
	if payload != nil {
		env.Payload = mustMarshal(payload)
	}

	n.logger.Info("rpc sent", "type", string(msgType), "to", target.String(), "rpc_id", shortID(rpcID))
	if err := n.transport.Send(target.UDPAddr(), env); err != nil {
		return Envelope{}, err
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-ctx.Done():
		n.logger.Debug("rpc timed out", "type", string(msgType), "to", target.String(), "rpc_id", shortID(rpcID))
		return Envelope{}, ctx.Err()
	}
}

// deliverResponse routes an incoming reply to whichever goroutine is
// waiting on its RPC ID, if any. A reply for an RPC ID we're not waiting
// on (already timed out, or a duplicate/forged packet) is silently
// dropped.
func (n *Node) deliverResponse(env Envelope) {
	n.pendingMu.Lock()
	ch, ok := n.pending[env.RPCID]
	n.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- env:
	default: // already delivered or no longer being read; drop
	}
}

// ping is the internal, bool-returning form used by the eviction check.
func (n *Node) ping(ctx context.Context, target Contact) bool {
	_, err := n.sendAndWait(ctx, target, MsgPing, nil)
	return err == nil
}

// Ping sends a PING RPC and reports whether the target replied before ctx
// expired.
func (n *Node) Ping(ctx context.Context, target Contact) bool {
	return n.ping(ctx, target)
}

func (n *Node) replyPong(from *net.UDPAddr, req Envelope) {
	resp := Envelope{Type: MsgPong, RPCID: req.RPCID, SenderID: n.ID}
	if err := n.transport.Send(from, resp); err != nil {
		n.logger.Warn("failed to send PONG", "to", from.String(), "error", err)
	}
}

// FindNode sends a FIND_NODE RPC asking `target` for the contacts it knows
// of closest to `queryID`, per paper Sec. 2.3.
func (n *Node) FindNode(ctx context.Context, target Contact, queryID ID) ([]Contact, error) {
	resp, err := n.sendAndWait(ctx, target, MsgFindNode, FindNodeRequest{Target: queryID})
	if err != nil {
		return nil, err
	}
	var payload FindNodeResponse
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		return nil, fmt.Errorf("kademlia: decode find_node response from %s: %w", target, err)
	}
	return payload.Contacts, nil
}

func (n *Node) replyFindNode(from *net.UDPAddr, req Envelope) {
	var payload FindNodeRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		n.logger.Warn("malformed FIND_NODE request", "from", from.String(), "error", err)
		return
	}
	closest := n.closestExcluding(payload.Target, req.SenderID)
	resp := Envelope{
		Type:     MsgFindNodeResp,
		RPCID:    req.RPCID,
		SenderID: n.ID,
		Payload:  mustMarshal(FindNodeResponse{Contacts: closest}),
	}
	if err := n.transport.Send(from, resp); err != nil {
		n.logger.Warn("failed to send FIND_NODE_RESP", "to", from.String(), "error", err)
	}
}

// storeRPC sends one STORE RPC (paper Sec. 2.3) and reports whether the
// target kept the value. Node.Store (values.go) calls it for each of the k
// closest nodes.
func (n *Node) storeRPC(ctx context.Context, target Contact, key ID, value []byte) error {
	resp, err := n.sendAndWait(ctx, target, MsgStore, StoreRequest{Key: key, Value: value})
	if err != nil {
		return err
	}
	var payload StoreResponse
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		return fmt.Errorf("kademlia: decode store response from %s: %w", target, err)
	}
	if !payload.Stored {
		return fmt.Errorf("kademlia: %s refused STORE: %s", target, payload.Reason)
	}
	return nil
}

// SIMPLIFICATION: any node may STORE anything under any key. There's no
// check that the value is legitimate or that the sender owns it -- the
// same openness as the paper, with none of the hardening a public network
// would need.
func (n *Node) replyStore(from *net.UDPAddr, req Envelope) {
	var payload StoreRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		n.logger.Warn("malformed STORE request", "from", from.String(), "error", err)
		return
	}
	resp := StoreResponse{Stored: true}
	if len(payload.Value) > maxValueSize {
		resp = StoreResponse{Reason: fmt.Sprintf("value is %d bytes, limit is %d", len(payload.Value), maxValueSize)}
	} else {
		n.store.put(payload.Key, payload.Value)
		n.logger.Info("value stored", "key", payload.Key.String(), "bytes", len(payload.Value))
	}
	n.reply(from, req, MsgStoreResp, resp)
}

// findValueRPC sends one FIND_VALUE RPC. The reply carries either values
// (the target holds the key) or the target's closest contacts.
func (n *Node) findValueRPC(ctx context.Context, target Contact, key ID) (values [][]byte, contacts []Contact, err error) {
	resp, err := n.sendAndWait(ctx, target, MsgFindValue, FindValueRequest{Key: key})
	if err != nil {
		return nil, nil, err
	}
	var payload FindValueResponse
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		return nil, nil, fmt.Errorf("kademlia: decode find_value response from %s: %w", target, err)
	}
	return payload.Values, payload.Contacts, nil
}

func (n *Node) replyFindValue(from *net.UDPAddr, req Envelope) {
	var payload FindValueRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		n.logger.Warn("malformed FIND_VALUE request", "from", from.String(), "error", err)
		return
	}
	var resp FindValueResponse
	if values := n.store.get(payload.Key); len(values) > 0 {
		resp.Values = values
	} else {
		resp.Contacts = n.closestExcluding(payload.Key, req.SenderID)
	}
	n.reply(from, req, MsgFindValueResp, resp)
}

// closestExcluding returns the k contacts closest to target, leaving out
// the requester. handleMessage has already added the requester to our
// table, so it would otherwise often appear in its own answer -- a wasted
// slot that, with a small k, hides a real node from it.
//
// SIMPLIFICATION: contacts are passed on exactly as we observed them. A
// 127.0.0.1 contact (another node on this machine that reached us over
// loopback) means "this machine" only to us; handed to a peer on another
// machine, it points that peer at itself. This assumes one node per device
// on a multi-device network, or every node on one machine -- never a mix
// (docs/09-multi-device.md, F2), so no loopback contact ever meets a remote
// requester.
func (n *Node) closestExcluding(target, requester ID) []Contact {
	out := n.rt.Closest(target, n.k+1)
	for i, c := range out {
		if c.ID == requester {
			out = append(out[:i], out[i+1:]...)
			break
		}
	}
	return out[:min(len(out), n.k)]
}

// reply sends a response envelope that echoes the request's RPC ID.
func (n *Node) reply(to *net.UDPAddr, req Envelope, msgType MessageType, payload any) {
	resp := Envelope{Type: msgType, RPCID: req.RPCID, SenderID: n.ID, Payload: mustMarshal(payload)}
	if err := n.transport.Send(to, resp); err != nil {
		n.logger.Warn("failed to send reply", "type", string(msgType), "to", to.String(), "error", err)
	}
}

// Bootstrap joins the network given only the UDP address of one node that
// is already in it. Two steps (docs/04-bootstrap-and-join.md):
//
//  1. PING the address. We only know an IP:port, not who is listening
//     there; the PONG's envelope carries the responder's real node ID, and
//     the normal receive path (handleMessage -> touchContact) files it in
//     our routing table under that real ID. No reply means the bootstrap
//     node is unreachable, which we report instead of carrying on.
//  2. Look up our own ID (paper Sec. 2.3, "To join the network..."). That
//     lookup fills our routing table from the FIND_NODE replies, and every
//     node we query records us as an RPC sender, which is how the network
//     learns we exist.
//
// SIMPLIFICATION: the paper additionally has a freshly-joined node refresh
// every bucket farther away than its closest neighbor. We skip that extra
// refresh pass; the self-lookup alone is enough to populate a useful
// routing table at this project's scale.
func (n *Node) Bootstrap(ctx context.Context, addr *net.UDPAddr) (*LookupResult, error) {
	unknown := Contact{IP: addr.IP.String(), Port: addr.Port} // ID is all-zero: not known yet
	pingCtx, cancel := context.WithTimeout(ctx, n.rpcTimeout)
	pong, err := n.sendAndWait(pingCtx, unknown, MsgPing, nil)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("kademlia: bootstrap node %s did not answer PING: %w", addr, err)
	}
	if pong.SenderID == n.ID {
		// The PONG carries our own ID: we can't tell ourselves from a twin.
		return nil, fmt.Errorf("kademlia: the node at %s has the same ID as this node (%s): "+
			"it is this node itself, or another node started with the same ID", addr, shortID(n.ID))
	}
	n.logger.Info("bootstrap node identified", "id", pong.SenderID.String(), "addr", addr.String())
	return n.Lookup(ctx, n.ID)
}
