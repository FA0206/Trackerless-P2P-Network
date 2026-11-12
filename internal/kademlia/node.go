package kademlia

import (
	"context"
	"encoding/json"
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
)

// Node is a running Kademlia participant: its identity, its routing table,
// its UDP transport, and the in-flight RPC bookkeeping needed to match
// replies to requests. This is the type cmd/node wires up and the type
// lookup.go's iterative lookup is a method of.
type Node struct {
	ID         ID
	rt         *RoutingTable
	transport  *Transport
	logger     *slog.Logger
	k          int
	alpha      int
	rpcTimeout time.Duration

	pendingMu sync.Mutex
	pending   map[ID]chan Envelope // rpc id -> channel the waiting caller reads from
}

// NewNode creates a node listening on the given UDP port. Pass logger=nil
// to get a default stdout logger (see logging.go).
func NewNode(id ID, port, k, alpha int, rpcTimeout time.Duration, logger *slog.Logger) (*Node, error) {
	if logger == nil {
		logger = NewLogger(id, slog.LevelInfo)
	}
	transport, err := NewTransport(port, logger)
	if err != nil {
		return nil, err
	}
	return &Node{
		ID:         id,
		rt:         NewRoutingTable(id, k),
		transport:  transport,
		logger:     logger,
		k:          k,
		alpha:      alpha,
		rpcTimeout: rpcTimeout,
		pending:    make(map[ID]chan Envelope),
	}, nil
}

// Addr returns the UDP address this node's socket is bound to.
func (n *Node) Addr() *net.UDPAddr {
	return n.transport.LocalAddr()
}

// Self returns this node's own contact info, as other nodes would need to
// reach it. We hardcode the loopback address: this project targets a
// localhost demo (multiple processes on one machine, see the README), and
// has no NAT traversal or external-address discovery, which is out of
// scope.
func (n *Node) Self() Contact {
	return Contact{ID: n.ID, IP: "127.0.0.1", Port: n.Addr().Port}
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
func (n *Node) handleMessage(from *net.UDPAddr, env Envelope) {
	sender := ContactFromUDPAddr(env.SenderID, from)
	n.logger.Info("rpc received", "type", string(env.Type), "from", sender.String(), "rpc_id", shortID(env.RPCID))

	n.touchContact(sender)

	switch env.Type {
	case MsgPing:
		n.replyPong(from, env)
	case MsgFindNode:
		n.replyFindNode(from, env)
	case MsgPong, MsgFindNodeResp:
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
	closest := n.rt.Closest(payload.Target, n.k)
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
	return n.Lookup(ctx, n.ID)
}
