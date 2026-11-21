package kademlia

import (
	"bytes"
	"context"
	"slices"
	"sort"
)

// LookupResult summarizes one iterative lookup, including the bookkeeping
// the demo script reports (round and RPC counts) alongside the actual
// answer.
type LookupResult struct {
	Target     ID
	Closest    []Contact // up to k contacts, nearest-to-target first
	Rounds     int       // number of parallel alpha-wide RPC batches issued
	RPCsSent   int
	RPCsFailed int

	// FIND_VALUE only (see FindValue in values.go):
	Values         [][]byte  // distinct values returned; nil if none found
	ValuesFrom     []Contact // which nodes returned them
	FromLocalStore bool      // answered from this node's own store, no RPCs
}

// lookupCandidate tracks one contact's status within a single Lookup call.
type lookupCandidate struct {
	contact Contact
	queried bool
	failed  bool
}

// lookupMode says which RPC a lookup sends and when it stops.
type lookupMode int

const (
	modeFindNode      lookupMode = iota // FIND_NODE until the k closest live nodes have answered
	modeFindValue                       // FIND_VALUE; stop after the first round that returns values
	modeFindAllValues                   // FIND_VALUE to every one of the k closest live nodes; merge all values
)

// Lookup is Kademlia's node-lookup procedure (paper Sec. 2.3): the
// "most important procedure a Kademlia participant must perform", used to
// find the k nodes closest to `target`, and the basis for both Bootstrap
// (looking up one's own ID) and for FIND_NODE-only routing in general.
//
// Algorithm, following the paper: start from the alpha closest contacts
// this node already knows of (from its own routing table). Query them in
// parallel with FIND_NODE. Merge every contact they return into a
// candidate list. Repeat, each round querying up to alpha of the k
// closest candidates that haven't been queried yet, until the k closest
// have all answered. A candidate that fails (times out) is "removed from
// consideration" (Sec. 2.3): it stops counting towards those k, and the
// next-closest candidate takes its place. That stopping rule -- "queried
// and gotten responses from the k closest nodes it has seen" -- is what
// makes this terminate in O(log n) rounds instead of needing a fixed
// iteration count, and it's why the answer is the k closest LIVE nodes
// even when some of the closest contacts are dead (edge case B1).
//
// SIMPLIFICATION: the paper additionally short-circuits early once a round
// returns nothing closer than what's already known UNLESS the k closest
// aren't all queried yet, in which case it queries the rest of them
// anyway. Our loop always queries every one of the k closest exactly once
// regardless of whether earlier rounds improved on the best distance, which
// is simpler to implement correctly and produces the same final answer, at
// the cost of occasionally issuing a few more RPCs than the paper's
// version would in the "lookup already converged early" case.
//
// SIMPLIFICATION: rounds are synchronous -- each round waits for all of its
// RPCs (including timeouts) before picking the next batch, whereas the
// paper keeps alpha RPCs in flight and continues as soon as any reply
// lands. A dead contact therefore costs one full rpcTimeout per round.
func (n *Node) Lookup(ctx context.Context, target ID) (*LookupResult, error) {
	return n.iterativeFind(ctx, target, modeFindNode)
}

// iterativeFind is the lookup algorithm shared by FIND_NODE and FIND_VALUE
// (paper Sec. 2.3: "value lookups use FIND_VALUE rather than FIND_NODE
// RPCs. Moreover, the procedure halts immediately when any node returns
// the value"). In modeFindValue each RPC is a FIND_VALUE, and the lookup
// stops after the first round in which any node returned values. In
// modeFindAllValues it doesn't stop early: it asks every one of the k
// closest live nodes and merges whatever values they hold (see
// FindAllValues in values.go).
func (n *Node) iterativeFind(ctx context.Context, target ID, mode lookupMode) (*LookupResult, error) {
	result := &LookupResult{Target: target}
	n.noteLookup(target) // this bucket needs no refresh for a while (refresh.go)
	findValue := mode != modeFindNode

	candidates := make(map[ID]*lookupCandidate)
	var order []ID // candidate IDs, kept sorted nearest-to-target first

	// Seed with the whole routing table, nearest first, not just the k
	// closest: only the working set below is ever queried, so farther
	// contacts cost nothing unless closer ones fail. Without them, a lookup
	// whose k closest contacts are all dead has nothing left to try
	// (edge case B1 found lookups returning nobody while half the network
	// was alive). Stale contacts (stale.go) are left out, unless nothing
	// fresh is left at all: then this node's own network was probably down,
	// and the stale contacts are its best bet (paper Sec. 4.1).
	seeds := n.rt.Closest(target, n.rt.Size())
	useStale := len(seeds) == 0
	if useStale {
		seeds = n.rt.ClosestIncludingStale(target, n.rt.Size())
	}

	addCandidate := func(c Contact) {
		if c.ID == n.ID {
			return // never chase ourselves
		}
		if _, exists := candidates[c.ID]; exists {
			return
		}
		if !useStale && n.stale.isStale(c.ID) {
			// Another node still lists it, but it failed us several
			// times in a row: don't spend a timeout on it again. The
			// paper "avoids sending them any further RPCs" (Sec. 4.1).
			return
		}
		candidates[c.ID] = &lookupCandidate{contact: c}
		order = append(order, c.ID)
	}
	for _, c := range seeds {
		addCandidate(c)
	}

	sortByDistance := func() {
		sort.Slice(order, func(i, j int) bool {
			return Less(Xor(candidates[order[i]].contact.ID, target), Xor(candidates[order[j]].contact.ID, target))
		})
	}
	sortByDistance()

	// workingSet is the k closest candidates that haven't failed, nearest
	// first. The lookup queries only these, and is done once all of them
	// have answered: the paper's "queried and gotten responses from the k
	// closest nodes it has seen". Failed candidates are "removed from
	// consideration" (Sec. 2.3) but stay in `candidates`, so a later reply
	// that mentions them can't bring them back for a second timeout.
	workingSet := func() []*lookupCandidate {
		var ws []*lookupCandidate
		for _, id := range order {
			if c := candidates[id]; !c.failed {
				ws = append(ws, c)
				if len(ws) == n.k {
					break
				}
			}
		}
		return ws
	}

	type rpcOutcome struct {
		candidate *lookupCandidate
		contacts  []Contact
		values    [][]byte
		err       error
	}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		var batch []*lookupCandidate
		for _, c := range workingSet() {
			if len(batch) >= n.alpha {
				break
			}
			if !c.queried {
				batch = append(batch, c)
			}
		}
		if len(batch) == 0 {
			break // the k closest live candidates have all answered: converged
		}

		result.Rounds++
		n.logger.Debug("lookup round starting", "target", target.String(), "round", result.Rounds, "batch", len(batch))

		outcomes := make(chan rpcOutcome, len(batch))
		for _, c := range batch {
			c.queried = true
			go func(c *lookupCandidate) {
				rpcCtx, cancel := context.WithTimeout(ctx, n.rpcTimeout)
				defer cancel()
				o := rpcOutcome{candidate: c}
				if findValue {
					o.values, o.contacts, o.err = n.findValueRPC(rpcCtx, c.contact, target)
				} else {
					o.contacts, o.err = n.FindNode(rpcCtx, c.contact, target)
				}
				outcomes <- o
			}(c)
		}

		for i := 0; i < len(batch); i++ {
			o := <-outcomes
			result.RPCsSent++
			if o.err != nil {
				o.candidate.failed = true
				result.RPCsFailed++
				n.logger.Debug("lookup rpc failed", "target", target.String(), "contact", o.candidate.contact.String(), "error", o.err)
				continue
			}
			if len(o.values) > 0 {
				result.ValuesFrom = append(result.ValuesFrom, o.candidate.contact)
				result.Values = appendDistinct(result.Values, o.values)
			}
			for _, c := range o.contacts {
				addCandidate(c)
			}
		}
		// No trimming to k: when a candidate fails, the next-closest one
		// (perhaps one learned in an earlier round) moves into the working
		// set. The list stays small: at most the routing table plus k
		// contacts per reply.
		sortByDistance()

		if mode == modeFindValue && len(result.Values) > 0 {
			// FIND_VALUE found something: stop. Every reply in this round
			// was already collected (rounds are synchronous), so values
			// from several holders in the same round are merged.
			break
		}
	}

	for _, c := range workingSet() {
		result.Closest = append(result.Closest, c.contact)
	}
	if findValue {
		n.logger.Info("find_value finished", "key", target.String(), "values_found", len(result.Values),
			"rounds", result.Rounds, "rpcs_sent", result.RPCsSent, "rpcs_failed", result.RPCsFailed,
			"ask_all", mode == modeFindAllValues)
	} else {
		n.logger.Info("lookup converged", "target", target.String(), "rounds", result.Rounds,
			"rpcs_sent", result.RPCsSent, "rpcs_failed", result.RPCsFailed, "closest_found", len(result.Closest))
	}
	return result, nil
}

// appendDistinct appends each value in add that isn't already in vals.
func appendDistinct(vals, add [][]byte) [][]byte {
	for _, a := range add {
		if !slices.ContainsFunc(vals, func(v []byte) bool { return bytes.Equal(v, a) }) {
			vals = append(vals, a)
		}
	}
	return vals
}
