package transfer

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The cap is per node, not per connection: two writers sharing one uplink
// together take as long as one writer sending both loads.
func TestUplinkIsSharedAcrossWriters(t *testing.T) {
	const rate, chunk, chunksEach = 1 << 20, 64 << 10, 4 // 2 x 256 KiB at 1 MiB/s: >= 500 ms
	var u uplink
	u.setRate(rate)
	start := time.Now()
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range chunksEach {
				u.wait(chunk)
			}
		}()
	}
	wg.Wait()
	if got, min := time.Since(start), 2*chunksEach*chunk*time.Second/rate; got < min*9/10 {
		t.Errorf("took %s, a shared %d B/s uplink needs >= %s", got, rate, min)
	}
}

func TestUplinkUnlimitedByDefault(t *testing.T) {
	var u uplink
	start := time.Now()
	u.wait(100 << 20)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("an uncapped uplink waited %s", d)
	}
}

// --debug-upload-rate reaches the pieces a node serves: one leecher pulling
// a file from one capped seeder can't finish faster than size / rate.
func TestDebugUploadRateSlowsServing(t *testing.T) {
	const pieces, rate = 8, 2 << 20 // 512 KiB at 2 MiB/s: >= 250 ms
	peers := startNetwork(t, 3)
	dir := t.TempDir()
	src := writeRandomFile(t, dir, pieces*DefaultPieceSize)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	key, err := peers[0].peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	peers[0].peer.SetDebugUploadRate(rate)
	out := filepath.Join(dir, "out.bin")
	res, err := peers[1].peer.Download(ctx, key, out)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertSameFile(t, src, out)
	if min := pieces * DefaultPieceSize * time.Second / rate; res.Duration < min*9/10 {
		t.Errorf("took %s, a %d B/s uplink needs >= %s", res.Duration, rate, min)
	}
}
