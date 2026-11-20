package transfer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kademlia-demo/internal/kademlia"
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

// shortStall makes downloads give up after 1 s instead of 15 s.
func shortStall(t *testing.T) {
	old := stallTimeout
	stallTimeout = time.Second
	t.Cleanup(func() { stallTimeout = old })
}

// seedThenKill shares an 8-piece file from peers[0], slowed to 50 ms a
// piece, starts peers[1] downloading it into out, and stops peers[0] once
// peers[1] has at least two pieces. It returns the key, the source file,
// the download's result channel, and when the seeder was stopped.
func seedThenKill(t *testing.T, ctx context.Context, peers []testPeer, dir, out string) (kademlia.ID, string, chan error, time.Time) {
	t.Helper()
	src := writeRandomFile(t, dir, 8*DefaultPieceSize)
	key, err := peers[0].peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	peers[0].peer.SetDebugServeDelay(50 * time.Millisecond)
	errc := make(chan error, 1)
	go func() { _, err := peers[1].peer.Download(ctx, key, out); errc <- err }()
	for {
		if have, ok := peers[1].peer.lib.bitfield(key); ok && have.Count(8) >= 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	peers[0].stop()
	return key, src, errc, time.Now()
}

// waitGaveUp waits for a download that can't finish to give up.
func waitGaveUp(t *testing.T, errc chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("download succeeded although nobody had the missing pieces")
		}
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("download never gave up")
	}
	return nil
}

// If the only node holding the missing pieces dies, the download must give
// up after stallTimeout -- not keep waiting because bitfield polling looks
// like activity. Giving up keeps the partial file and keeps serving its
// pieces (docs/08-swarming.md, "Why giving up mustn't delete").
func TestDownloadGivesUpWhenNoOneHasTheRest(t *testing.T) {
	shortStall(t)
	peers := startNetwork(t, 3)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := filepath.Join(dir, "out.bin")
	key, _, errc, killed := seedThenKill(t, ctx, peers, dir, out)
	err := waitGaveUp(t, errc)
	if waited := time.Since(killed); waited > 5*time.Second {
		t.Errorf("gave up after %s; expected about stallTimeout (1s)", waited)
	}
	t.Logf("gave up after %s: %v", time.Since(killed).Round(time.Millisecond), err)

	if _, err := os.Stat(out); err != nil {
		t.Errorf("giving up deleted the partial file: %v", err)
	}
	have, ok := peers[1].peer.lib.bitfield(key)
	if !ok || have.Count(8) < 2 {
		t.Fatalf("giving up stopped serving the pieces it had (still in library: %v)", ok)
	}
	for i := 0; i < 8; i++ {
		if !have.Has(i) {
			continue
		}
		if _, err := peers[1].peer.lib.readPiece(key, i); err != nil {
			t.Errorf("piece %d can't be served after giving up: %v", i, err)
		}
	}
}

// The 3-vs-6 case: two leechers lose the seeder before they hold every
// piece between them. The one that gives up first must keep serving, so
// the other ends with exactly the same pieces instead of being cut off.
func TestGivingUpKeepsServing(t *testing.T) {
	shortStall(t)
	peers := startNetwork(t, 3)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	key, _, errc, _ := seedThenKill(t, ctx, peers, dir, filepath.Join(dir, "first.bin"))
	waitGaveUp(t, errc)
	first, ok := peers[1].peer.lib.bitfield(key)
	if !ok {
		t.Fatal("the first leecher forgot its pieces when it gave up")
	}

	res, err := peers[2].peer.Download(ctx, key, filepath.Join(dir, "second.bin"))
	if err == nil {
		t.Fatal("second download succeeded although nobody had the missing pieces")
	}
	second, ok := peers[2].peer.lib.bitfield(key)
	if !ok {
		t.Fatalf("the second leecher got nothing from the first: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("leechers ended with different pieces: %d vs %d of 8", first.Count(8), second.Count(8))
	}
	firstAddr := ProviderRecord{IP: "127.0.0.1", Port: peers[1].peer.TCPPort()}.Addr()
	if got := res.PiecesFrom[firstAddr]; got != first.Count(8) {
		t.Errorf("second leecher got %d pieces from the first, want all %d it had", got, first.Count(8))
	}
}

// Fetching a key again on the node whose download gave up continues from
// the pieces already on disk once a source is back.
func TestFetchAgainResumes(t *testing.T) {
	shortStall(t)
	peers := startNetwork(t, 3)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := filepath.Join(dir, "out.bin")
	key, src, errc, _ := seedThenKill(t, ctx, peers, dir, out)
	waitGaveUp(t, errc)
	before, ok := peers[1].peer.lib.bitfield(key)
	if !ok {
		t.Fatal("the leecher forgot its pieces when it gave up")
	}
	k := before.Count(8)

	// Only the original path resumes: anywhere else would be a second copy.
	_, err := peers[1].peer.Download(ctx, key, filepath.Join(dir, "elsewhere.bin"))
	if err == nil || !strings.Contains(err.Error(), "stopped partial download at") {
		t.Errorf("fetch into another path: got %v, want the stopped-partial error", err)
	}

	// A file that can't be opened for writing (but is still there) keeps
	// its entry: its pieces stay served, and a later fetch can resume.
	if os.Geteuid() != 0 { // root ignores file permissions
		if err := os.Chmod(out, 0o444); err != nil {
			t.Fatal(err)
		}
		_, err := peers[1].peer.Download(ctx, key, out)
		if err == nil || !strings.Contains(err.Error(), "still serving its pieces") {
			t.Errorf("resume into a read-only file: got %v, want the still-serving error", err)
		}
		if _, ok := peers[1].peer.lib.bitfield(key); !ok {
			t.Error("a read-only partial file was dropped from the library")
		}
		if err := os.Chmod(out, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The same bytes shared from another node have the same key.
	if _, err := peers[2].peer.Share(ctx, src); err != nil {
		t.Fatalf("Share again: %v", err)
	}
	res, err := peers[1].peer.Download(ctx, key, out)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	assertSameFile(t, src, out)
	if res.AlreadyHad != k {
		t.Errorf("AlreadyHad = %d, want %d", res.AlreadyHad, k)
	}
	fetched := 0
	for _, n := range res.PiecesFrom {
		fetched += n
	}
	if fetched != 8-k {
		t.Errorf("resume fetched %d pieces, want only the %d missing ones", fetched, 8-k)
	}
	t.Logf("resumed with %d of 8 on disk, fetched %d", k, fetched)

	_, err = peers[1].peer.Download(ctx, key, out)
	if err == nil || !strings.Contains(err.Error(), "already in the library") {
		t.Errorf("fetch of a complete file: got %v, want already in the library", err)
	}
}

// A resume must not trust the file at the path: if it was changed after
// the download stopped (here: replaced by the user's own bytes), the
// resume is refused, the file is left alone, and the entry is dropped so
// its pieces aren't served.
func TestResumeRefusesAChangedFile(t *testing.T) {
	shortStall(t)
	peers := startNetwork(t, 3)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := filepath.Join(dir, "out.bin")
	key, src, errc, _ := seedThenKill(t, ctx, peers, dir, out)
	waitGaveUp(t, errc)
	mine := bytes.Repeat([]byte("USERDATA"), 8*DefaultPieceSize/8)
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, mine, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := peers[2].peer.Share(ctx, src); err != nil {
		t.Fatalf("Share again: %v", err)
	}

	_, err := peers[1].peer.Download(ctx, key, out)
	if err == nil || !strings.Contains(err.Error(), "changed since the download stopped") {
		t.Fatalf("resume into a replaced file: got %v, want refused", err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, mine) {
		t.Error("the refused resume wrote into the user's file")
	}
	if _, ok := peers[1].peer.lib.bitfield(key); ok {
		t.Error("the entry for the replaced file is still in the library (still served)")
	}
}

// A download that gives up before its first piece has nothing to serve or
// resume, so it leaves no file and no library entry behind.
func TestGivingUpWithNoPiecesLeavesNothing(t *testing.T) {
	shortStall(t)
	peers := startNetwork(t, 3)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	src := writeRandomFile(t, dir, 8*DefaultPieceSize)
	key, err := peers[0].peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	peers[0].peer.SetDebugServeDelay(time.Minute) // no piece ever arrives

	out := filepath.Join(dir, "out.bin")
	errc := make(chan error, 1)
	go func() { _, err := peers[1].peer.Download(ctx, key, out); errc <- err }()
	for { // the manifest arrived: the partial entry exists
		if _, ok := peers[1].peer.lib.bitfield(key); ok {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	peers[0].stop()
	waitGaveUp(t, errc)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a download that gave up with no pieces left its file behind (stat: %v)", err)
	}
	if _, ok := peers[1].peer.lib.bitfield(key); ok {
		t.Error("a download that gave up with no pieces is still in the library")
	}
}

// If the caller stops waiting after the last piece is written, while the
// download waits for its announce, the file is complete: Download must
// report success, not a failure that "fetch again" can't resume.
func TestCancelAfterLastPieceStillCompletes(t *testing.T) {
	peers := startNetwork(t, 3)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	src := writeRandomFile(t, dir, 4*DefaultPieceSize)
	key, err := peers[0].peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	peers[2].stop() // a dead node in every lookup: the announce waits an RPC timeout

	dctx, dcancel := context.WithCancel(ctx)
	defer dcancel()
	out := filepath.Join(dir, "out.bin")
	errc := make(chan error, 1)
	go func() { _, err := peers[1].peer.Download(dctx, key, out); errc <- err }()
	for {
		if have, ok := peers[1].peer.lib.bitfield(key); ok && have.Count(4) == 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	dcancel()
	if err := <-errc; err != nil {
		t.Fatalf("cancelled after the last piece: got %v, want success", err)
	}
	assertSameFile(t, src, out)
}
