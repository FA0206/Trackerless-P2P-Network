package kademlia

import (
	"context"
	"errors"
	"fmt"
)

// StoreResult reports where a value ended up.
type StoreResult struct {
	Key           ID
	StoredAt      []Contact     // remote nodes that acknowledged the STORE
	StoredLocally bool          // this node is itself one of the k closest
	Failed        int           // STORE RPCs that timed out or were refused
	Lookup        *LookupResult // the lookup that chose the k closest nodes
}

// Store publishes (key, value) on the k nodes closest to key (paper
// Sec. 2.3: "To store a (key,value) pair, a participant locates the k
// closest nodes to the key and sends them STORE RPCs"). If this node is
// itself among the k closest, it keeps a copy too: the lookup never
// returns ourselves, so we have to check that separately.
//
// This node also remembers that it published (key, value), and publishes
// it again every Config.RepublishInterval (republish.go, paper Sec. 2.5),
// so the value survives its holders dying and moves to closer nodes that
// join later. It remembers even if this attempt fails: the next republish
// may succeed.
func (n *Node) Store(ctx context.Context, key ID, value []byte) (*StoreResult, error) {
	if len(value) > maxValueSize {
		return nil, fmt.Errorf("kademlia: value is %d bytes, limit is %d", len(value), maxValueSize)
	}
	n.published.add(key, value)
	lookup, err := n.Lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	result := n.storeOnClosest(ctx, key, value, lookup)
	n.logger.Info("store complete", "key", key.String(), "stored_at", len(result.StoredAt),
		"stored_locally", result.StoredLocally, "failed", result.Failed)
	if len(result.StoredAt) == 0 && !result.StoredLocally {
		return result, errors.New("kademlia: no node stored the value")
	}
	return result, nil
}

// storeOnClosest sends STORE(key, value) to the nodes a lookup for key
// found, keeping a local copy too if this node is among the k closest.
// Shared by Store and the republish loop. It logs nothing; callers do.
func (n *Node) storeOnClosest(ctx context.Context, key ID, value []byte, lookup *LookupResult) *StoreResult {
	result := &StoreResult{Key: key, Lookup: lookup}

	targets := lookup.Closest
	if selfAmongClosest(n.ID, key, targets, n.k) {
		n.store.put(key, value)
		result.StoredLocally = true
		if len(targets) == n.k {
			targets = targets[:n.k-1] // keep the total number of copies at k
		}
	}

	type outcome struct {
		contact Contact
		err     error
	}
	outcomes := make(chan outcome, len(targets))
	for _, c := range targets {
		go func(c Contact) {
			rpcCtx, cancel := context.WithTimeout(ctx, n.rpcTimeout)
			defer cancel()
			outcomes <- outcome{c, n.storeRPC(rpcCtx, c, key, value)}
		}(c)
	}
	for range targets {
		o := <-outcomes
		if o.err != nil {
			result.Failed++
			n.logger.Debug("store rpc failed", "key", key.String(), "contact", o.contact.String(), "error", o.err)
			continue
		}
		result.StoredAt = append(result.StoredAt, o.contact)
	}
	return result
}

// selfAmongClosest reports whether self would be one of the k closest
// nodes to key, given the k closest *other* nodes a lookup found.
func selfAmongClosest(self, key ID, closest []Contact, k int) bool {
	if len(closest) < k {
		return true
	}
	return Less(Xor(self, key), Xor(closest[len(closest)-1].ID, key))
}

// FindValue retrieves every value stored under key (paper Sec. 2.3: a
// lookup using FIND_VALUE that "halts immediately when any node returns
// the value"). It checks this node's own store first; otherwise it runs
// the iterative lookup in FIND_VALUE mode. A result with no Values means
// no node on the lookup path held the key.
//
// SIMPLIFICATION: no caching. The paper has the searcher store the value
// at the closest node on the path that didn't have it, so popular keys
// spread toward where lookups come from. We skip that.
func (n *Node) FindValue(ctx context.Context, key ID) (*LookupResult, error) {
	if local := n.store.get(key); len(local) > 0 {
		n.logger.Info("find_value answered from local store", "key", key.String(), "values_found", len(local))
		return &LookupResult{Target: key, Values: local, ValuesFrom: []Contact{n.Self()}, FromLocalStore: true}, nil
	}
	return n.iterativeFind(ctx, key, modeFindValue)
}

// FindAllValues collects every value stored under key by the k closest
// live nodes, plus any this node holds itself, merged without duplicates.
// It's for keys that hold a *list* that keeps growing, like a file's
// provider records (internal/transfer): each provider stores its record on
// the k nodes closest to the key *as it saw them*, and a provider that is
// itself among them keeps one copy and sends only k-1, so no single holder
// -- this node included -- is guaranteed to have them all.
//
// FindValue would get that wrong twice: it answers from this node's own
// store without asking anyone ("find_value answered from local store"),
// and otherwise stops at the first round that returns any value. Then a
// downloader that holds a record (say its own, or a dead seeder's) never
// learns about providers whose records went elsewhere. So this runs the
// lookup to completion in FIND_VALUE mode and asks every one of the k
// closest live nodes, at the cost of a few more RPCs. `get` keeps using
// FindValue: a text value under a name is one value, and any copy is it.
func (n *Node) FindAllValues(ctx context.Context, key ID) (*LookupResult, error) {
	result, err := n.iterativeFind(ctx, key, modeFindAllValues)
	if local := n.store.get(key); len(local) > 0 {
		result.Values = appendDistinct(local, result.Values)
		result.ValuesFrom = append([]Contact{n.Self()}, result.ValuesFrom...)
	}
	return result, err
}

// StoredKeyCount returns how many distinct keys this node holds values
// for on behalf of the network.
func (n *Node) StoredKeyCount() int {
	return n.store.keyCount()
}

// StoredValues returns a copy of every (key, values) pair this node holds
// on behalf of the network, values oldest first. It's for inspection only
// (the per-node HTTP API, internal/nodeapi); the DHT itself never needs it.
func (n *Node) StoredValues() map[ID][][]byte {
	return n.store.snapshot()
}
