package transfer

import "math/rand"

// holderView is the downloader's view of one live provider, as far as the
// piece picker is concerned: what it has, and whether it's free to take a
// request right now.
type holderView struct {
	have Bitfield // nil if we haven't fetched its bitfield yet
	idle bool
}

// pickPiece chooses the next piece to request and who to request it from:
// among pieces in `need` that
// at least one idle holder has, the one held by the fewest holders
// (rarest first); ties broken at random. The provider is a random idle
// holder of that piece. Returns ok=false if nothing can be assigned now.
// Pure function: no I/O, no shared state.
func pickPiece(need Bitfield, n int, holders []holderView, rng *rand.Rand) (piece, holder int, ok bool) {
	best, ties := -1, 0
	bestCount := 0
	for i := 0; i < n; i++ {
		if !need.Has(i) {
			continue
		}
		count, idleHolders := 0, 0
		for _, h := range holders {
			if h.have != nil && h.have.Has(i) {
				count++
				if h.idle {
					idleHolders++
				}
			}
		}
		if idleHolders == 0 {
			continue
		}
		switch {
		case best == -1 || count < bestCount:
			best, bestCount, ties = i, count, 1
		case count == bestCount:
			ties++
			if rng.Intn(ties) == 0 { // reservoir sampling: each tie equally likely
				best = i
			}
		}
	}
	if best == -1 {
		return 0, 0, false
	}
	chosen, seen := -1, 0
	for j, h := range holders {
		if h.idle && h.have != nil && h.have.Has(best) {
			seen++
			if rng.Intn(seen) == 0 {
				chosen = j
			}
		}
	}
	return best, chosen, true
}
