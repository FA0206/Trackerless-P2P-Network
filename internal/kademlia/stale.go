package kademlia

import "sync"

// staleTracker counts consecutive failed RPCs per node ID (paper Sec. 4.1:
// "When a contact fails to respond to 5 RPCs in a row, it is considered
// stale"). A stale node is skipped: lookups don't query it, FIND_NODE
// replies don't hand it out, and a full bucket replaces it without a ping.
// Any message from the node clears its count ("removed from consideration
// until and unless they do respond", Sec. 2.3).
//
// It covers every node this node sends RPCs to, not only routing-table
// contacts. After a mass death, most dead nodes a lookup meets are ones it
// heard about in other nodes' replies, never in its own table (edge case
// B1, docs/EDGE-CASES.md). Counting only table contacts barely helped.
//
// No network I/O here, like bucket.go: Node reports failures (sendAndWait)
// and successes (handleMessage).
//
// SIMPLIFICATION: the paper also keeps a replacement cache of contacts
// that arrived while a bucket was full, and backs off exponentially before
// retrying an unresponsive contact. We have neither: a stale node gets no
// RPCs from lookups until it sends us something, and a stale table contact
// is replaced only when a new contact for its bucket next messages us.
// See docs/02-routing-table-and-kbuckets.md.
type staleTracker struct {
	mu         sync.Mutex
	staleAfter int        // failures in a row that make a node stale; <= 0 turns tracking off
	failures   map[ID]int // only nodes with at least one unanswered RPC since they last spoke
}

// maxTracked bounds the map. Entries go away when a node speaks, but a
// node that dies is never heard from again, so without a bound the map
// would grow with every dead ID ever met. Past the bound, new IDs aren't
// tracked (they cost timeouts as before); already-tracked ones still are.
const maxTracked = 4096

func newStaleTracker(staleAfter int) *staleTracker {
	return &staleTracker{staleAfter: staleAfter, failures: make(map[ID]int)}
}

// failed records one unanswered RPC to id. It reports whether this was the
// failure that made id stale, so the caller can log that moment once.
func (s *staleTracker) failed(id ID) (becameStale bool) {
	if s == nil || id == (ID{}) {
		return false // the zero ID is Bootstrap's not-yet-identified seed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.staleAfter <= 0 {
		return false
	}
	if _, tracked := s.failures[id]; !tracked && len(s.failures) >= maxTracked {
		return false
	}
	s.failures[id]++
	return s.failures[id] == s.staleAfter
}

// heardFrom clears id's count: it sent us a message, so it's alive. It
// reports whether id had been stale, so the caller can log the recovery.
func (s *staleTracker) heardFrom(id ID) (wasStale bool) {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wasStale = s.isStaleLocked(id)
	delete(s.failures, id)
	return wasStale
}

// isStale reports whether id failed staleAfter RPCs in a row and hasn't
// been heard from since. Safe to call on a nil tracker (never stale).
func (s *staleTracker) isStale(id ID) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isStaleLocked(id)
}

func (s *staleTracker) isStaleLocked(id ID) bool {
	return s.staleAfter > 0 && s.failures[id] >= s.staleAfter
}
