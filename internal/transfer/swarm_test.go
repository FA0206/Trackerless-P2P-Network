package transfer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Three complete providers, each slowed to 60 ms per piece: a download that
// really runs in parallel takes ~3 rounds for 9 pieces; a sequential one
// would take 9.
func TestParallelDownloadFromThreeProviders(t *testing.T) {
	const pieces, delay = 9, 60 * time.Millisecond
	peers := startNetwork(t, 5)
	dir := t.TempDir()
	src := writeRandomFile(t, dir, pieces*DefaultPieceSize)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	key, err := peers[0].peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	for _, i := range []int{1, 2} {
		if _, err := peers[i].peer.Download(ctx, key, filepath.Join(dir, fmt.Sprintf("p%d.bin", i))); err != nil {
			t.Fatalf("Download by peer %d: %v", i, err)
		}
	}
	for _, i := range []int{0, 1, 2} {
		peers[i].peer.SetDebugServeDelay(delay)
	}

	out := filepath.Join(dir, "out.bin")
	res, err := peers[3].peer.Download(ctx, key, out)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertSameFile(t, src, out)
	if res.MaxInFlight < 3 {
		t.Errorf("at most %d piece requests were in flight at once; expected 3 (one per provider)", res.MaxInFlight)
	}
	if len(res.PiecesFrom) != 3 {
		t.Errorf("pieces came from %d providers, want 3: %v", len(res.PiecesFrom), res.PiecesFrom)
	}
	if sequential := pieces * delay; res.Duration > sequential*3/4 {
		t.Errorf("took %s; one-at-a-time would take ~%s, so the fetches didn't overlap", res.Duration, sequential)
	}
	t.Logf("9 pieces from 3 providers in %s (sequential: >= %s), max in flight %d, split %v",
		res.Duration.Round(time.Millisecond), pieces*delay, res.MaxInFlight, res.PiecesFrom)
}

// The scenario DRY-RUN part 3 shows failing on main: the seeder dies before
// any leecher has finished. Here pieces have spread first -- the leechers
// TOGETHER hold every piece, though none holds all of them -- so every
// leecher finishes from the others.
func TestPartialSeedingSurvivesSeederDeath(t *testing.T) {
	const pieces, delay = 16, 40 * time.Millisecond
	peers := startNetwork(t, 6)
	seeder, leechers := peers[0], peers[1:5]
	dir := t.TempDir()
	src := writeRandomFile(t, dir, pieces*DefaultPieceSize)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	key, err := seeder.peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	for _, p := range peers {
		p.peer.SetDebugServeDelay(delay)
	}

	type outcome struct {
		i   int
		res *DownloadResult
		err error
	}
	done := make(chan outcome, len(leechers))
	for i, l := range leechers {
		go func(i int, l testPeer) {
			res, err := l.peer.Download(ctx, key, filepath.Join(dir, fmt.Sprintf("l%d.bin", i)))
			done <- outcome{i, res, err}
		}(i, l)
	}

	// Kill the seeder at the first moment the pieces have spread.
	deadline := time.Now().Add(20 * time.Second)
	var atKill []int
	for {
		union := NewBitfield(pieces)
		atKill = atKill[:0]
		complete := false
		for _, l := range leechers {
			have, ok := l.peer.lib.bitfield(key)
			n := 0
			if ok {
				n = have.Count(pieces)
				for i := 0; i < pieces; i++ {
					if have.Has(i) {
						union.Set(i)
					}
				}
			}
			atKill = append(atKill, n)
			complete = complete || n == pieces
		}
		if complete {
			t.Fatalf("a leecher finished before the leechers together held every piece (counts %v); timing assumption broke", atKill)
		}
		if union.Count(pieces) == pieces {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pieces never spread to cover the file (counts %v)", atKill)
		}
		time.Sleep(2 * time.Millisecond)
	}
	seeder.stop()
	t.Logf("seeder stopped; leechers held %v of %d pieces, together all of them", atKill, pieces)

	for range leechers {
		select {
		case o := <-done:
			if o.err != nil {
				t.Errorf("leecher %d failed after the seeder died: %v", o.i, o.err)
				continue
			}
			assertSameFile(t, src, filepath.Join(dir, fmt.Sprintf("l%d.bin", o.i)))
			t.Logf("leecher %d: done in %s, pieces from %v", o.i, o.res.Duration.Round(time.Millisecond), o.res.PiecesFrom)
		case <-ctx.Done():
			t.Fatal("leechers didn't finish after the seeder died")
		}
	}
}
