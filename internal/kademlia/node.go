package kademlia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Default protocol parameters. k and alpha are named exactly as in the
// paper (Sec. 2.2 and 2.3): k is the replication/bucket-size parameter,
// alpha is the lookup concurrency parameter.
const (
	DefaultK          = 20
	DefaultAlpha      = 3
	DefaultRPCTimeout = 2 * time.Second
	// DefaultStaleAfter is the paper's number (Sec. 4.1): "When a contact
	// fails to respond to 5 RPCs in a row, it is considered stale."
	DefaultStaleAfter = 5
)

// Node is a running Kademlia participant: its identity, its routing table,
// its UDP transport, and the in-flight RPC bookkeeping needed to match
// replies to requests. This is the type cmd/node wires up and the type
// lookup.go's iterative lookup is a method of.
type Node struct {
	ID          ID
	rt          *RoutingTable
	stale       *staleTracker // nodes that failed several RPCs in a row (stale.go)
	store       *valueStore
	published   *publications // what this node itself Stored, for republishing
	republish   time.Duration // republish interval; <= 0 = off (republish.go)
	refresh     time.Duration // periodic bucket refresh interval; <= 0 = off (refresh.go)
	joinRefresh bool          // refresh buckets at the end of Bootstrap (refresh.go)
	lookups     bucketLookups // when a lookup last targeted each bucket (refresh.go)
	stop        chan struct{} // closed by Close: stops the republish loop
	stopOnce    sync.Once
	transport   *Transport
	logger      *slog.Logger
	advertiseIP string
	k           int
	alpha       int
	rpcTimeout  time.Duration

	pendingMu sync.Mutex
	pending   map[ID]chan Envelope // rpc id -> channel the waiting caller reads from
}

// Config holds everything needed to create a Node. Zero values get
// defaults, so tests can fill in only what they care about.
type Config struct {
	ID   ID
	Port int // UDP port; 0 lets the OS pick a free one
	// AdvertiseIP is the address other nodes should use to reach this one,
	// e.g. in provider records for file sharing. Default "127.0.0.1".
	//
	// SIMPLIFICATION: it's configured by hand. There's no discovery of
	// our own external address and no NAT traversal, so for a
	// multi-device demo every node must be given its LAN IP here.
	AdvertiseIP string
	K           int           // default DefaultK
	Alpha       int           // default DefaultAlpha
	RPCTimeout  time.Duration // default DefaultRPCTimeout
	StaleAfter  int           // failed RPCs in a row before a node is stale; default DefaultStaleAfter, < 0 = never
	// RepublishInterval: how often this node stores again what it
	// published and what it holds (republish.go, paper Sec. 2.5).
	// Default DefaultRepublishInterval (1 h); < 0 turns republishing off.
	RepublishInterval time.Duration
	// RefreshInterval: how often to refresh buckets no lookup touched
	// during the interval (refresh.go, paper Sec. 2.3). Default
	// DefaultRefreshInterval (1 h); < 0 turns the periodic refresh off.
	// The refresh at join (Bootstrap) runs regardless.
	RefreshInterval time.Duration
	// SkipJoinRefresh turns off the refresh at the end of Bootstrap. Only
	// for experiments that compare with and without it (edge case A4).
	SkipJoinRefresh bool
	Logger          *slog.Logger // default NewLogger(ID, Info)
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
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	if cfg.RepublishInterval == 0 {
		cfg.RepublishInterval = DefaultRepublishInterval
	}
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}
	transport, err := NewTransport(cfg.Port, cfg.Logger)
	if err != nil {
		return nil, err
	}
	stale := newStaleTracker(cfg.StaleAfter)
	rt := NewRoutingTable(cfg.ID, cfg.K)
	rt.setStaleCheck(stale.isStale)
	return &Node{
		ID:          cfg.ID,
		rt:          rt,
		stale:       stale,
		published:   newPublications(),
		republish:   cfg.RepublishInterval,
		refresh:     cfg.RefreshInterval,
		joinRefresh: !cfg.SkipJoinRefresh,
		stop:        make(chan struct{}),
		store:       newValueStore(),
		transport:   transport,
		logger:      cfg.Logger,
		advertiseIP: cfg.AdvertiseIP,
		k:           cfg.K,
		alpha:       cfg.Alpha,
		rpcTimeout:  cfg.RPCTimeout,
		pending:     make(map[ID]chan Envelope),
	}, nil
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
	if n.republish > 0 {
		go n.republishLoop(n.republish)
	}
	if n.refresh > 0 {
		go n.refreshLoop(n.refresh)
	}
}

// Close stops the republish loop and shuts down the node's socket. Safe to
// call more than once.
func (n *Node) Close() error {
	n.stopOnce.Do(func() { close(n.stop) })
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

// IsStale reports whether a node is stale: it failed several RPCs in a
// row (Config.StaleAfter) and hasn't been heard from since. Stale contacts
// stay in the routing table (and in RoutingTableSnapshot), but lookups and
// replies skip them. For display: the console's `table`, the API.
func (n *Node) IsStale(id ID) bool {
	return n.stale.isStale(id)
}

// handleMessage is the single entry point for every received packet: it
// updates the routing table for whoever sent it (paper Sec. 2.2: "when a
// node receives any message... it updates the appropriate k-bucket for the
// sender's node ID"), then dispatches on message type.
func (n *Node) handleMessage(from *net.UDPAddr, env Envelope) {
	sender := ContactFromUDPAddr(env.SenderID, from)
	n.logger.Info("rpc received", "type", string(env.Type), "from", sender.String(), "rpc_id", shortID(env.RPCID))

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

// touchContact applies the k-bucket update rule (bucket.go) and, if the
// bucket turns out to be full, performs the paper's ping-then-decide
// eviction check (Sec. 2.2) asynchronously so handling this message never
// blocks on a network round trip to some other contact entirely.
func (n *Node) touchContact(c Contact) {
	if n.stale.heardFrom(c.ID) { // it spoke: clear its failures first
		n.logger.Info("stale contact answered again", "contact", c.String())
	}
	outcome, stale := n.rt.Update(c)
	switch outcome {
	case Seen:
		n.logger.Debug("bucket updated", "contact", c.String())
	case ReplacedStale:
		n.logger.Info("stale contact replaced", "evicted", stale.String(), "replacement", c.String())
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
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			n.noteFailure(target) // no reply in time; a cancelled caller isn't the target's fault
		}
		return Envelope{}, ctx.Err()
	}
}

// noteFailure counts one unanswered RPC to target (paper Sec. 4.1) and
// logs the moment it becomes stale. It counts every node we send RPCs to,
// in our routing table or not (see stale.go for why).
func (n *Node) noteFailure(target Contact) {
	if n.stale.failed(target.ID) {
		n.logger.Info("contact marked stale", "contact", target.String())
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
		return nil, fmt.Errorf("kademlia: bootstrap address %s is this node itself", addr)
	}
	n.logger.Info("bootstrap node identified", "id", pong.SenderID.String(), "addr", addr.String())
	self, err := n.Lookup(ctx, n.ID)
	if err != nil {
		return self, err
	}
	// Paper Sec. 2.3: "Finally, u refreshes all k-buckets further away than
	// its closest neighbor" (refresh.go). Best effort: a failed refresh
	// lookup doesn't fail the join.
	if n.joinRefresh {
		n.refreshBuckets(ctx, n.closestNeighborBucket(), time.Time{}, "join")
	}
	return self, nil
}
