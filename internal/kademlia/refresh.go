package kademlia

import (
	"context"
	"crypto/rand"
	"sync"
	"time"
)

// DefaultRefreshInterval is the paper's hourly bucket refresh (Sec. 2.3).
const DefaultRefreshInterval = time.Hour

// Bucket refresh (paper Sec. 2.3). Contacts spread only as a side effect
// of lookups, so a part of the ID space nobody looks up goes unvisited:
// a quiet group joined by a single bridge node never learns of the other
// side (edge case A3), a newcomer with k = 1 knows almost nobody (A4), and
// a node never tests, and so never stops handing out, its dead contacts
// (B1, SEED=4). The paper's answer:
//
//   - at join: "u refreshes all k-buckets further away than its closest
//     neighbor" (Bootstrap calls refreshBuckets after its self-lookup);
//   - periodically: "each node refreshes any bucket to which it has not
//     performed a node lookup in the past hour".
//
// "Refreshing means picking a random ID in the bucket's range and
// performing a node search for that ID." Every lookup records the bucket
// its target falls in (bucketLookups), so buckets that normal traffic
// already keeps fresh cost nothing.
//
// Which buckets: from the one holding our closest neighbor up to 159.
// A lower bucket would need a node closer to us than our closest
// neighbor; a lookup for such an ID only finds the neighbors we know.
// The paper's split tree never creates those buckets, so this is the same
// set it refreshes.
//
// SIMPLIFICATION: one timer per node, not one per bucket. Every
// RefreshInterval, a single pass refreshes each bucket idle for that
// long, one lookup at a time, so a bucket can wait up to two intervals.
// Harmless at this scale; refresh traffic belongs in the control-bytes
// count (README simplifications table).

// bucketLookups remembers when a lookup last targeted each bucket.
type bucketLookups struct {
	mu   sync.Mutex
	last [IDBits]time.Time
}

func (b *bucketLookups) touch(i int, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last[i] = now
}

// staleSince reports whether bucket i had no lookup after cutoff.
func (b *bucketLookups) staleSince(i int, cutoff time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.last[i].After(cutoff)
}

// noteLookup records a lookup for target (called by iterativeFind). A
// lookup for our own ID belongs to no bucket.
func (n *Node) noteLookup(target ID) {
	if target != n.ID {
		n.lookups.touch(BucketIndex(n.ID, target), time.Now())
	}
}

// RandomIDInBucket returns a random ID whose XOR distance from self has
// its highest set bit at position i, i.e. one that belongs in bucket i
// (BucketIndex(self, id) == i).
func RandomIDInBucket(self ID, i int) ID {
	var d ID
	_, _ = rand.Read(d[:]) // crypto/rand.Read never fails (Go 1.24+)
	// Bit i is the highest set bit of d: clear everything above it, set it.
	byteIdx := IDBytes - 1 - i/8 // ID is big-endian: bit 0 is the last byte's lowest bit
	bit := byte(1) << (i % 8)
	for j := 0; j < byteIdx; j++ {
		d[j] = 0
	}
	d[byteIdx] &= bit - 1
	d[byteIdx] |= bit
	return Xor(self, d)
}

// closestNeighborBucket returns the bucket index of our closest known
// contact, or -1 if the table is empty.
func (n *Node) closestNeighborBucket() int {
	closest := n.rt.ClosestIncludingStale(n.ID, 1)
	if len(closest) == 0 {
		return -1
	}
	return BucketIndex(n.ID, closest[0].ID)
}

// refreshBuckets looks up a random ID in each bucket from `from` to 159
// that had no lookup since cutoff (a zero cutoff refreshes all of them),
// one bucket at a time, and logs one summary line. A failed lookup is
// counted, not fatal: refresh is best effort.
func (n *Node) refreshBuckets(ctx context.Context, from int, cutoff time.Time, reason string) {
	if from < 0 {
		return
	}
	refreshed, rpcs, failed := 0, 0, 0
	before := n.rt.Size()
	for i := IDBits - 1; i > from; i-- {
		if !cutoff.IsZero() && !n.lookups.staleSince(i, cutoff) {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		res, err := n.Lookup(ctx, RandomIDInBucket(n.ID, i))
		refreshed++
		if res != nil {
			rpcs += res.RPCsSent
			failed += res.RPCsFailed
		}
		if err != nil {
			n.logger.Debug("bucket refresh lookup failed", "bucket", i, "error", err)
		}
	}
	if refreshed > 0 {
		n.logger.Info("buckets refreshed", "reason", reason, "buckets", refreshed,
			"rpcs_sent", rpcs, "rpcs_failed", failed,
			"routing_table_size", n.rt.Size(), "learned", n.rt.Size()-before)
	}
}

// refreshLoop refreshes, every interval, each bucket from our closest
// neighbor's up that no lookup touched during the interval.
func (n *Node) refreshLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-n.stop; cancel() }()
	for {
		select {
		case <-n.stop:
			return
		case <-ticker.C:
			// Include the closest neighbor's own bucket: it can hold
			// other nodes too.
			n.refreshBuckets(ctx, n.closestNeighborBucket()-1, time.Now().Add(-interval), "periodic")
		}
	}
}
