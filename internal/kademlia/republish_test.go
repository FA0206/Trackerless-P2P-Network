package kademlia

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestStoreRemembersWhenEachKeyWasRefreshed(t *testing.T) {
	s := newValueStore()
	clock := time.Unix(1000, 0)
	s.now = func() time.Time { return clock }
	old, fresh := idFromByte(1), idFromByte(2)
	s.put(old, []byte("a"))
	clock = clock.Add(time.Hour)
	s.put(fresh, []byte("b"))

	due := s.notRefreshedSince(clock.Add(-30 * time.Minute))
	if len(due) != 1 || string(due[old][0]) != "a" {
		t.Fatalf("due for republish: %v, want only the key not stored for an hour", due)
	}
	due[old][0][0] = 'X' // a copy: changing it mustn't change the store
	if string(s.get(old)[0]) != "a" {
		t.Error("notRefreshedSince returned the store's own slices")
	}
}

func TestPublicationsDeduplicate(t *testing.T) {
	p := newPublications()
	p.add(idFromByte(1), []byte("v"))
	p.add(idFromByte(1), []byte("v"))
	p.add(idFromByte(1), []byte("w"))
	if got := p.snapshot()[idFromByte(1)]; len(got) != 2 {
		t.Errorf("got %d values, want 2 distinct", len(got))
	}
}

// Edge case B2 in Go: every node holding a value dies at once, while the
// publisher lives. Its republish stores the value on the k closest live
// nodes, so another node finds it again without anyone re-storing by hand.
func TestPublisherRepublishSurvivesAllHoldersDying(t *testing.T) {
	const size, k = 8, 3
	nodes := republishNetwork(t, size, k, 500*time.Millisecond)
	key := HashID([]byte("b2 in go"))
	publisher := pickNotAmong(nodes, trueClosest(nodes, key, k))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := publisher.Store(ctx, key, []byte("still here")); err != nil {
		t.Fatal(err)
	}
	holders := trueClosest(nodes, key, k)
	var live []*Node
	for _, n := range nodes {
		if holders[n.ID] {
			n.Close()
		} else {
			live = append(live, n)
		}
	}

	newHolders := trueClosest(live, key, k)
	waitFor(t, 20*time.Second, "the value to reach the k closest live nodes", func() bool {
		for _, n := range live {
			if newHolders[n.ID] && n != publisher && len(n.store.get(key)) == 0 {
				return false
			}
		}
		return true
	})
	asker := pickNotAmong(live, newHolders)
	if asker == publisher {
		asker = pickNotAmong(liveExcept(live, publisher), newHolders)
	}
	res, err := asker.FindValue(ctx, key)
	if err != nil || len(res.Values) != 1 || string(res.Values[0]) != "still here" {
		t.Fatalf("FindValue after republish: %v, %v", res, err)
	}
}

// Edge case B3 in Go: a node joins closer to the key than any holder. The
// next republish (from the holders, the publisher has left) stores the
// value on it.
func TestHolderRepublishReachesACloserNewcomer(t *testing.T) {
	const size, k = 6, 2
	nodes := republishNetwork(t, size, k, 500*time.Millisecond)
	key := HashID([]byte("b3 in go"))
	publisher := pickNotAmong(nodes, trueClosest(nodes, key, k))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := publisher.Store(ctx, key, []byte("move me")); err != nil {
		t.Fatal(err)
	}
	publisher.Close() // only the holders can republish now

	near := key
	near[IDBytes-1] ^= 1 // differs from the key in the last bit: closer than anyone
	newcomer := startRepublishNode(t, near, k, 500*time.Millisecond)
	via := nodes[0]
	if via == publisher {
		via = nodes[1]
	}
	if _, err := newcomer.Bootstrap(ctx, via.Self().UDPAddr()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "a holder's republish to reach the newcomer", func() bool {
		return len(newcomer.store.get(key)) == 1
	})
}

// A holder that just got a STORE for a key doesn't republish it in the
// next interval (paper Sec. 2.5).
func TestHolderSkipsKeysStoredRecently(t *testing.T) {
	s := newValueStore()
	s.put(idFromByte(1), []byte("just stored"))
	if due := s.notRefreshedSince(time.Now().Add(-time.Minute)); len(due) != 0 {
		t.Errorf("a key stored just now is due for republish: %v", due)
	}
}

func TestCloseStopsRepublishing(t *testing.T) {
	n := startRepublishNode(t, idFromByte(9), 3, 10*time.Millisecond)
	n.Close()
	n.Close() // twice is fine
	select {
	case <-n.stop:
	default:
		t.Fatal("Close didn't close the stop channel")
	}
}

func republishNetwork(t *testing.T, size, k int, every time.Duration) []*Node {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nodes := make([]*Node, size)
	for i := range nodes {
		nodes[i] = startRepublishNode(t, HashID([]byte{'r', byte(i)}), k, every)
		if i > 0 {
			if _, err := nodes[i].Bootstrap(ctx, nodes[0].Self().UDPAddr()); err != nil {
				t.Fatalf("node %d bootstrap: %v", i, err)
			}
		}
	}
	return nodes
}

func startRepublishNode(t *testing.T, id ID, k int, every time.Duration) *Node {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	if os.Getenv("KADEMLIA_TEST_LOGS") != "" {
		logger = NewLogger(id, slog.LevelDebug)
	}
	n, err := NewNode(Config{ID: id, K: k, RPCTimeout: 300 * time.Millisecond, StaleAfter: 1,
		RepublishInterval: every, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	n.Start()
	t.Cleanup(func() { n.Close() })
	return n
}

// pickNotAmong returns the first node whose ID isn't in the set.
func pickNotAmong(nodes []*Node, set map[ID]bool) *Node {
	for _, n := range nodes {
		if !set[n.ID] {
			return n
		}
	}
	return nil
}

func waitFor(t *testing.T, limit time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", limit, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
