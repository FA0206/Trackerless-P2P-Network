package kademlia

import "sync"

// Bucket is a single k-bucket: an ordered list of up to k contacts, oldest
// (least-recently-seen) first and newest (most-recently-seen) last. See
// docs/02-routing-table-and-kbuckets.md and paper Sec. 2.2/2.4.
//
// Eviction needs a network round-trip (ping the oldest contact before
// dropping it), which this type can't do by itself. So Update doesn't
// silently evict: when the bucket is full of contacts other than the one
// being seen, it hands the caller the stale candidate and lets the caller
// (Node, in node.go) ping it and report back via ReplaceStale or KeepAlive.
type Bucket struct {
	mu       sync.Mutex
	capacity int
	contacts []Contact // index 0 = least recently seen, last = most recently seen
}

// NewBucket creates an empty bucket holding at most `capacity` contacts
// (the paper's k, Sec. 2.2).
func NewBucket(capacity int) *Bucket {
	return &Bucket{capacity: capacity}
}

// UpdateOutcome describes what Update did, so the caller knows whether any
// follow-up action (a ping) is needed.
type UpdateOutcome int

const (
	// Seen means the contact was new-inserted or moved to the tail; no
	// further action needed.
	Seen UpdateOutcome = iota
	// FullNeedsPing means the bucket is at capacity and does not already
	// contain this contact. The caller must ping StaleCandidate and then
	// call ReplaceStale or KeepAlive with the result.
	FullNeedsPing
)

// Update records that `c` was just seen (a message arrived from it). Per
// paper Sec. 2.2:
//   - if c is already in the bucket, move it to the tail.
//   - else if the bucket isn't full, insert it at the tail.
//   - else return FullNeedsPing with the least-recently-seen contact, so the
//     caller can ping it before deciding whether to evict it.
func (b *Bucket) Update(c Contact) (outcome UpdateOutcome, staleCandidate Contact) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, existing := range b.contacts {
		if existing.ID == c.ID {
			b.contacts = append(b.contacts[:i], b.contacts[i+1:]...)
			b.contacts = append(b.contacts, c)
			return Seen, Contact{}
		}
	}

	if len(b.contacts) < b.capacity {
		b.contacts = append(b.contacts, c)
		return Seen, Contact{}
	}

	return FullNeedsPing, b.contacts[0]
}

// KeepAlive is called when a ping to the least-recently-seen contact (the
// one Update returned as staleCandidate) succeeded: it's still alive, so it
// moves to the tail and the new, unsolicited contact is discarded. This is
// the "live nodes are never removed" rule from paper Sec. 2.2.
func (b *Bucket) KeepAlive(stale Contact) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, existing := range b.contacts {
		if existing.ID == stale.ID {
			b.contacts = append(b.contacts[:i], b.contacts[i+1:]...)
			b.contacts = append(b.contacts, existing)
			return
		}
	}
}

// ReplaceStale is called when a ping to `stale` failed (no response): it's
// evicted and `replacement` takes its place at the tail. Returns false if
// `stale` was no longer present (e.g. it was already replaced by a
// concurrent update), in which case nothing is inserted -- the bucket
// stays at capacity rather than growing past it.
func (b *Bucket) ReplaceStale(stale, replacement Contact) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, existing := range b.contacts {
		if existing.ID == stale.ID {
			b.contacts = append(b.contacts[:i], b.contacts[i+1:]...)
			b.contacts = append(b.contacts, replacement)
			return true
		}
	}
	return false
}

// Contacts returns a snapshot of the bucket's contents, oldest first.
func (b *Bucket) Contacts() []Contact {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Contact, len(b.contacts))
	copy(out, b.contacts)
	return out
}

// Len returns the number of contacts currently in the bucket.
func (b *Bucket) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.contacts)
}
