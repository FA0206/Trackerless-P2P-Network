package kademlia

import (
	"context"
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
}

// lookupCandidate tracks one contact's status within a single Lookup call.
type lookupCandidate struct {
	contact Contact
	queried bool
	failed  bool
}

// Lookup is Kademlia's node-lookup procedure (paper Sec. 2.3): the
// "most important procedure a Kademlia participant must perform", used to
// find the k nodes closest to `target`, and the basis for both Bootstrap
// (looking up one's own ID) and for FIND_NODE-only routing in general.
//
// Algorithm, following the paper: start from the alpha closest contacts
// this node already knows of (from its own routing table). Query them in
// parallel with FIND_NODE. Merge every contact they return into a
// candidate list, keeping only the k closest seen so far. Repeat, each
// round querying up to alpha of the k-closest candidates that haven't
// been queried yet, until every one of the k closest has been queried (or
// failed). That exact stopping rule -- "queried and gotten responses from
// the k closest nodes it has seen" -- is what makes this terminate in
// O(log n) rounds instead of needing a fixed iteration count.
//
// SIMPLIFICATION: the paper additionally short-circuits early once a round
// returns nothing closer than what's already known UNLESS the k closest
// aren't all queried yet, in which case it queries the rest of them
// anyway. Our loop always queries every one of the k closest exactly once
// regardless of whether earlier rounds improved on the best distance, which
// is simpler to implement correctly and produces the same final answer, at
// the cost of occasionally issuing a few more RPCs than the paper's
// version would in the "lookup already converged early" case. See
// docs/05-iterative-parallel-lookup.md.
func (n *Node) Lookup(ctx context.Context, target ID) (*LookupResult, error) {
	return n.lookup(ctx, target, nil)
}

// lookup is Lookup's implementation, with room for extra starting
// candidates that aren't (yet) in the routing table -- used by Bootstrap
// to seed the very first round with a contact whose real ID isn't known
// yet. See Bootstrap's doc comment for why that distinction matters.
func (n *Node) lookup(ctx context.Context, target ID, extraSeeds []Contact) (*LookupResult, error) {
	result := &LookupResult{Target: target}

	candidates := make(map[ID]*lookupCandidate)
	var order []ID // candidate IDs, kept sorted nearest-to-target first

	addCandidate := func(c Contact) {
		if c.ID == n.ID {
			return // never chase ourselves
		}
		if _, exists := candidates[c.ID]; exists {
			return
		}
		candidates[c.ID] = &lookupCandidate{contact: c}
		order = append(order, c.ID)
	}
	for _, c := range n.rt.Closest(target, n.k) {
		addCandidate(c)
	}
	for _, c := range extraSeeds {
		addCandidate(c)
	}

	sortByDistance := func() {
		sort.Slice(order, func(i, j int) bool {
			return Less(Xor(candidates[order[i]].contact.ID, target), Xor(candidates[order[j]].contact.ID, target))
		})
	}
	sortByDistance()

	type rpcOutcome struct {
		candidate *lookupCandidate
		contacts  []Contact
		err       error
	}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		var batch []*lookupCandidate
		for _, id := range order {
			if len(batch) >= n.alpha {
				break
			}
			if c := candidates[id]; !c.queried {
				batch = append(batch, c)
			}
		}
		if len(batch) == 0 {
			break // every one of the k closest candidates has been queried: converged
		}

		result.Rounds++
		n.logger.Debug("lookup round starting", "target", target.String(), "round", result.Rounds, "batch", len(batch))

		outcomes := make(chan rpcOutcome, len(batch))
		for _, c := range batch {
			c.queried = true
			go func(c *lookupCandidate) {
				rpcCtx, cancel := context.WithTimeout(ctx, n.rpcTimeout)
				defer cancel()
				contacts, err := n.FindNode(rpcCtx, c.contact, target)
				outcomes <- rpcOutcome{candidate: c, contacts: contacts, err: err}
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
			for _, c := range o.contacts {
				addCandidate(c)
			}
		}

		sortByDistance()
		// Trim to the k closest: anything farther can't end up in the
		// final answer and would only add bookkeeping to later rounds.
		if len(order) > n.k {
			for _, id := range order[n.k:] {
				delete(candidates, id)
			}
			order = order[:n.k]
		}
	}

	for _, id := range order {
		if c := candidates[id]; !c.failed {
			result.Closest = append(result.Closest, c.contact)
		}
	}
	n.logger.Info("lookup converged", "target", target.String(), "rounds", result.Rounds,
		"rpcs_sent", result.RPCsSent, "rpcs_failed", result.RPCsFailed, "closest_found", len(result.Closest))
	return result, nil
}
