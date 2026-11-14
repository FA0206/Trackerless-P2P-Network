package kademlia

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"
)

// DefaultRepublishInterval is the paper's hourly republish (Sec. 2.5).
const DefaultRepublishInterval = time.Hour

// Republishing (paper Sec. 2.5). Without it, a value lives exactly on the
// nodes chosen when it was stored: it's lost when they die, even if its
// publisher is still online (edge case B2), and nodes that join closer to
// its key never get it (B3). So, every RepublishInterval, each node does
// two passes:
//
//   - publisher: every (key, value) this node itself Stored is stored
//     again, through a fresh lookup. This brings a value back after all its
//     holders died at once, as long as the publisher lives.
//   - holder: every key this node holds for others is stored again, unless
//     it got a STORE for that key during the last interval. The paper's
//     reasoning: whoever sent that STORE sent it to the other k-1 closest
//     nodes too, so they're already up to date, and if republish timers
//     aren't synchronized, only one holder republishes each key per
//     interval. This keeps a value alive after its publisher left.
//
// Both go through a lookup, so the value also lands on nodes that joined
// closer to the key since the last time.
//
// SIMPLIFICATION: the paper republishes from holders hourly but from the
// original publisher only every 24 h (with values expiring after 24 h);
// here both use the same interval and nothing expires (store.go). It also
// skips the lookup before a holder's republish by using its split
// buckets (Sec. 2.5, second optimization), which our flat routing table
// can't do (docs/02-routing-table-and-kbuckets.md).

// publications is what this node itself published: key -> values. Only
// Store adds to it; nothing removes from it (a node publishes until it
// exits).
type publications struct {
	mu     sync.Mutex
	values map[ID][][]byte
}

func newPublications() *publications {
	return &publications{values: make(map[ID][][]byte)}
}

func (p *publications) add(key ID, value []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.values[key] {
		if bytes.Equal(v, value) {
			return
		}
	}
	p.values[key] = append(p.values[key], bytes.Clone(value))
}

func (p *publications) snapshot() map[ID][][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[ID][][]byte, len(p.values))
	for key, vals := range p.values {
		for _, v := range vals {
			out[key] = append(out[key], bytes.Clone(v))
		}
	}
	return out
}

// republishLoop runs both passes every interval until the node closes.
func (n *Node) republishLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-n.stop; cancel() }() // Close stops a republish in progress
	for {
		select {
		case <-n.stop:
			return
		case <-ticker.C:
			n.republishOnce(ctx, interval)
		}
	}
}

// republishOnce runs the publisher pass, then the holder pass. Keys are
// handled one at a time, in a fixed order, so the logs are reproducible.
func (n *Node) republishOnce(ctx context.Context, interval time.Duration) {
	cutoff := n.store.now().Add(-interval)
	for _, pass := range []struct {
		reason string
		values map[ID][][]byte
	}{
		{"publisher", n.published.snapshot()},
		{"holder", n.store.notRefreshedSince(cutoff)},
	} {
		for _, key := range sortedKeys(pass.values) {
			if ctx.Err() != nil {
				return
			}
			n.republishKey(ctx, key, pass.values[key], pass.reason)
		}
	}
}

// republishKey stores every value of one key again on the k closest nodes
// a fresh lookup finds: one lookup, then one STORE per value per node.
func (n *Node) republishKey(ctx context.Context, key ID, values [][]byte, reason string) {
	ctx, cancel := context.WithTimeout(ctx, republishKeyTimeout(n.rpcTimeout))
	defer cancel()
	lookup, err := n.Lookup(ctx, key)
	if err != nil {
		n.logger.Warn("republish failed", "key", key.String(), "reason", reason, "error", err)
		return
	}
	storedAt := map[ID]bool{}
	local, failed := false, 0
	for _, v := range values {
		res := n.storeOnClosest(ctx, key, v, lookup)
		for _, c := range res.StoredAt {
			storedAt[c.ID] = true
		}
		local = local || res.StoredLocally
		failed += res.Failed
	}
	n.logger.Info("republish complete", "key", key.String(), "reason", reason, "values", len(values),
		"stored_at", len(storedAt), "stored_locally", local, "failed", failed)
}

// republishKeyTimeout bounds one key's republish like the console bounds
// one operation (cmd/node: max(10s, 5 x rpc timeout)), so a lookup stuck
// on dead nodes can't hold up the other keys forever.
func republishKeyTimeout(rpcTimeout time.Duration) time.Duration {
	return max(10*time.Second, 5*rpcTimeout)
}

func sortedKeys(m map[ID][][]byte) []ID {
	keys := make([]ID, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return Less(keys[i], keys[j]) })
	return keys
}
