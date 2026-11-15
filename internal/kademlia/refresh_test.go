package kademlia

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestRandomIDInBucketLandsInThatBucket(t *testing.T) {
	self := HashID([]byte("self"))
	for i := 0; i < IDBits; i++ {
		for try := 0; try < 5; try++ {
			if got := BucketIndex(self, RandomIDInBucket(self, i)); got != i {
				t.Fatalf("RandomIDInBucket(self, %d) landed in bucket %d", i, got)
			}
		}
	}
}

func TestBucketLookupsRemembersTheLastLookup(t *testing.T) {
	var b bucketLookups
	now := time.Unix(1000, 0)
	b.touch(150, now)
	if b.staleSince(150, now.Add(-time.Minute)) {
		t.Error("bucket 150 was looked up after the cutoff, yet it's stale")
	}
	if !b.staleSince(150, now.Add(time.Minute)) || !b.staleSince(149, now.Add(-time.Hour)) {
		t.Error("a bucket with no lookup after the cutoff should be stale")
	}
}

// The paper's join (Sec. 2.3): after the self-lookup, "u refreshes all
// k-buckets further away than its closest neighbor". Each of those buckets
// must have had a lookup, and none without the join refresh.
func TestJoinRefreshLooksUpEveryFartherBucket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nodes := refreshNetwork(t, 8, 3, -1, false)
	for _, skip := range []bool{false, true} {
		n := startRefreshNode(t, HashID([]byte{'j', boolByte(skip)}), 3, -1, skip)
		if _, err := n.Bootstrap(ctx, nodes[3].Self().UDPAddr()); err != nil {
			t.Fatal(err)
		}
		from := n.closestNeighborBucket()
		missing := 0
		for i := from + 1; i < IDBits; i++ {
			if n.lookups.staleSince(i, time.Time{}) {
				missing++
			}
		}
		if !skip && missing != 0 {
			t.Errorf("join refresh skipped %d of the buckets above %d", missing, from)
		}
		if skip && missing != IDBits-1-from {
			t.Errorf("with SkipJoinRefresh, %d buckets above %d were looked up anyway", IDBits-1-from-missing, from)
		}
	}
}

// Edge case A3 in Go: two islands joined by one bridge node that nobody
// looks anything up through. With periodic refresh, the islands learn
// each other, so the bridge can die harmlessly.
func TestPeriodicRefreshConnectsIslandsBeforeTheBridgeDies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const every = 300 * time.Millisecond
	island := func(tag byte) []*Node {
		ns := make([]*Node, 3)
		for i := range ns {
			ns[i] = startRefreshNode(t, HashID([]byte{'i', tag, byte(i)}), 20, every, false)
			if i > 0 {
				if _, err := ns[i].Bootstrap(ctx, ns[0].Self().UDPAddr()); err != nil {
					t.Fatal(err)
				}
			}
		}
		return ns
	}
	a, b := island('a'), island('b')
	bridge := startRefreshNode(t, HashID([]byte("bridge")), 20, every, false)
	if _, err := bridge.Bootstrap(ctx, a[0].Self().UDPAddr()); err != nil {
		t.Fatal(err)
	}
	if !bridge.Ping(ctx, b[0].Self()) {
		t.Fatal("bridge can't reach island B")
	}

	waitFor(t, 15*time.Second, "an island-A node to learn every island-B node by refreshing", func() bool {
		known := 0
		for _, contacts := range a[1].RoutingTableSnapshot() {
			for _, c := range contacts {
				for _, other := range b {
					if c.ID == other.ID {
						known++
					}
				}
			}
		}
		return known == len(b)
	})
	bridge.Close()
	res, err := a[1].Lookup(ctx, b[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Closest {
		found = found || c.ID == b[2].ID
	}
	if !found {
		t.Errorf("after the bridge died, a lookup from island A didn't find island B: %v", res.Closest)
	}
}

func refreshNetwork(t *testing.T, size, k int, every time.Duration, skipJoin bool) []*Node {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nodes := make([]*Node, size)
	for i := range nodes {
		nodes[i] = startRefreshNode(t, HashID([]byte{'f', byte(i)}), k, every, skipJoin)
		if i > 0 {
			if _, err := nodes[i].Bootstrap(ctx, nodes[0].Self().UDPAddr()); err != nil {
				t.Fatalf("node %d bootstrap: %v", i, err)
			}
		}
	}
	return nodes
}

func startRefreshNode(t *testing.T, id ID, k int, every time.Duration, skipJoin bool) *Node {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	if os.Getenv("KADEMLIA_TEST_LOGS") != "" {
		logger = NewLogger(id, slog.LevelDebug)
	}
	n, err := NewNode(Config{ID: id, K: k, RPCTimeout: 300 * time.Millisecond, StaleAfter: 1,
		RefreshInterval: every, SkipJoinRefresh: skipJoin, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	n.Start()
	t.Cleanup(func() { n.Close() })
	return n
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
