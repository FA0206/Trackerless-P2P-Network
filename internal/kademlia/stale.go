package kademlia

import (
	"sync"
	"time"
)

// Default back-off for stale nodes. The paper gives no numbers (Sec. 4.1:
// "an exponentially increasing backoff interval"); these are ours.
const (
	DefaultStaleBackoff = 30 * time.Second // the first lock after a node goes stale
	maxStaleBackoff     = time.Hour        // the lock doubles on each failed retry, up to this
)

// staleTracker counts consecutive failed RPCs per node ID (paper Sec. 4.1:
// "When a contact fails to respond to 5 RPCs in a row, it is considered
// stale"). A stale node is skipped: lookups don't query it, FIND_NODE
// replies don't hand it out, and a full bucket replaces it without a ping.
// Any message from the node clears its count ("removed from consideration
// until and unless they do respond", Sec. 2.3).
//
// Back-off (Sec. 4.1: Kademlia "locks unresponsive contacts and avoids
// sending them any further RPCs for an exponentially increasing backoff
// interval"). Going stale locks a node for `backoff`. When the lock
// expires the node is on probation: it isn't stale, so lookups may ask it
// again. If it answers, its record is cleared. If that RPC fails too, it's
// locked again for twice as long, up to maxStaleBackoff. So a live node
// marked stale by mistake (lost packets) comes back within one lock, and
// a dead one costs one timeout per lock, ever more rarely.
//
// It covers every node this node sends RPCs to, not only routing-table
// contacts. After a mass death, most dead nodes a lookup meets are ones it
// heard about in other nodes' replies, never in its own table (edge case
// B1). Counting only table contacts barely helped.
//
// No network I/O here, like bucket.go: Node reports failures (sendAndWait)
// and successes (handleMessage).
//
// SIMPLIFICATION: the paper also keeps a replacement cache of contacts
// that arrived while a bucket was full. We don't: a stale table contact is
// replaced only when a new contact for its bucket next messages us.
type staleTracker struct {
	mu         sync.Mutex
	staleAfter int           // failures in a row that make a node stale; <= 0 turns tracking off
	backoff    time.Duration // the first lock; <= 0 = never retry (stale until it speaks)
	now        func() time.Time
	nodes      map[ID]*staleRecord // only nodes with an unanswered RPC since they last spoke
}

// staleRecord is one node's failures since it last spoke.
type staleRecord struct {
	failures int           // unanswered RPCs in a row
	lock     time.Duration // the current lock's length (0 until it first goes stale)
	until    time.Time     // stale until then; after it, on probation
}

// maxTracked bounds the map. Entries go away when a node speaks, but a
// node that dies is never heard from again, so without a bound the map
// would grow with every dead ID ever met. Past the bound, new IDs aren't
// tracked (they cost timeouts as before); already-tracked ones still are.
const maxTracked = 4096

func newStaleTracker(staleAfter int, backoff time.Duration) *staleTracker {
	return &staleTracker{staleAfter: staleAfter, backoff: backoff, now: time.Now,
		nodes: make(map[ID]*staleRecord)}
}

// failed records one unanswered RPC to id. If it locks id, it returns the
// lock's length, and whether this is the first lock (the node just went
// stale) rather than a failed retry, so the caller can log each once.
func (s *staleTracker) failed(id ID) (lockedFor time.Duration, first bool) {
	if s == nil || id == (ID{}) {
		return 0, false // the zero ID is Bootstrap's not-yet-identified seed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.staleAfter <= 0 {
		return 0, false
	}
	r, tracked := s.nodes[id]
	if !tracked {
		if len(s.nodes) >= maxTracked {
			return 0, false
		}
		r = &staleRecord{}
		s.nodes[id] = r
	}
	r.failures++
	if r.failures < s.staleAfter {
		return 0, false
	}
	first = r.lock == 0
	switch {
	case s.backoff <= 0:
		r.lock = maxStaleBackoff
		r.until = time.Time{} // never expires: stale until it speaks
		return 0, first
	case first:
		r.lock = s.backoff
	default:
		r.lock = min(2*r.lock, maxStaleBackoff)
	}
	r.until = s.now().Add(r.lock)
	return r.lock, first
}

// heardFrom clears id's record: it sent us a message, so it's alive. It
// reports whether id had been stale (locked or on probation), so the
// caller can log the recovery.
func (s *staleTracker) heardFrom(id ID) (wasStale bool) {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.nodes[id]
	wasStale = r != nil && r.lock > 0
	delete(s.nodes, id)
	return wasStale
}

// isStale reports whether id is locked: it failed staleAfter RPCs in a row,
// hasn't been heard from since, and its back-off hasn't run out. Safe to
// call on a nil tracker (never stale).
func (s *staleTracker) isStale(id ID) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.nodes[id]
	if s.staleAfter <= 0 || r == nil || r.failures < s.staleAfter {
		return false
	}
	return r.until.IsZero() || s.now().Before(r.until)
}
