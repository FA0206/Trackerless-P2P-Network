package transfer

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kademlia-demo/internal/kademlia"
)

type testPeer struct {
	node *kademlia.Node
	peer *Peer
}

func (tp testPeer) stop() {
	tp.peer.Close()
	tp.node.Close()
}

// startNetwork starts `size` nodes with file servers, all joined via the
// first. k=3 so provider records live on a subset of the nodes.
func startNetwork(t *testing.T, size int) []testPeer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	peers := make([]testPeer, size)
	for i := range peers {
		id := kademlia.HashID([]byte{'t', byte(i)})
		logger := slog.New(slog.DiscardHandler)
		if os.Getenv("KADEMLIA_TEST_LOGS") != "" {
			logger = kademlia.NewLogger(id, slog.LevelInfo)
		}
		node, err := kademlia.NewNode(kademlia.Config{ID: id, K: 3, RPCTimeout: 500 * time.Millisecond, Logger: logger})
		if err != nil {
			t.Fatal(err)
		}
		node.Start()
		peer, err := NewPeer(node, Config{IOTimeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		peers[i] = testPeer{node, peer}
		t.Cleanup(peers[i].stop)
		if i > 0 {
			if _, err := node.Bootstrap(ctx, peers[0].node.Self().UDPAddr()); err != nil {
				t.Fatalf("bootstrap %d: %v", i, err)
			}
		}
	}
	return peers
}

func assertSameFile(t *testing.T, want, got string) {
	t.Helper()
	a, _ := os.ReadFile(want)
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("downloaded file differs from the original (%d vs %d bytes)", len(b), len(a))
	}
}

func TestShareAndDownload(t *testing.T) {
	peers := startNetwork(t, 5)
	dir := t.TempDir()
	src := writeRandomFile(t, dir, 4*DefaultPieceSize+1234) // 5 pieces, short last one
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	key, err := peers[1].peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	out := filepath.Join(dir, "out.bin")
	res, err := peers[3].peer.Download(ctx, key, out)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertSameFile(t, src, out)
	if res.ProvidersFound != 1 || res.Pieces != 5 {
		t.Errorf("expected 1 provider and 5 pieces, got %d and %d", res.ProvidersFound, res.Pieces)
	}

	// The downloader announced itself, so the next downloader sees 2 providers.
	res2, err := peers[4].peer.Download(ctx, key, filepath.Join(dir, "out2.bin"))
	if err != nil {
		t.Fatalf("second Download: %v", err)
	}
	if res2.ProvidersFound != 2 || len(res2.PiecesFrom) != 2 {
		t.Errorf("expected pieces from 2 providers, got providers=%d piecesFrom=%v", res2.ProvidersFound, res2.PiecesFrom)
	}
}

func TestDownloadSurvivesSeederShutdown(t *testing.T) {
	peers := startNetwork(t, 5)
	dir := t.TempDir()
	src := writeRandomFile(t, dir, 6*DefaultPieceSize)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	key, err := peers[0].peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	if _, err := peers[1].peer.Download(ctx, key, filepath.Join(dir, "a.bin")); err != nil {
		t.Fatalf("first Download: %v", err)
	}

	seederAddr := ProviderRecord{IP: "127.0.0.1", Port: peers[0].peer.TCPPort()}.Addr()
	peers[0].stop() // the original seeder goes away; its provider record stays in the DHT

	out := filepath.Join(dir, "b.bin")
	res, err := peers[2].peer.Download(ctx, key, out)
	if err != nil {
		t.Fatalf("Download after seeder shutdown: %v", err)
	}
	assertSameFile(t, src, out)
	if len(res.FailedProviders) != 1 || res.FailedProviders[0] != seederAddr {
		t.Errorf("expected exactly the seeder (%s) to fail, got %v", seederAddr, res.FailedProviders)
	}
}

// A downloader can't rebuild the Merkle tree (it may not have every
// piece yet), so it serves each piece with the proof it received. Those
// must be exactly the seeder's proofs.
func TestDownloaderServesThePieceProofsItReceived(t *testing.T) {
	peers := startNetwork(t, 3)
	dir := t.TempDir()
	src := writeRandomFile(t, dir, 5*DefaultPieceSize)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	key, err := peers[0].peer.Share(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peers[1].peer.Download(ctx, key, filepath.Join(dir, "out.bin")); err != nil {
		t.Fatal(err)
	}
	seederTree := peers[0].peer.lib.files[key].tree
	m, _, _ := peers[1].peer.lib.manifestFor(key)
	for i := 0; i < 5; i++ {
		data, proof, err := peers[1].peer.lib.readPiece(key, i)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := seederTree.Proof(i)
		if len(proof) != len(want) {
			t.Fatalf("piece %d: the downloader serves a %d-hash proof, the seeder's has %d", i, len(proof), len(want))
		}
		for j := range want {
			if proof[j] != want[j] {
				t.Errorf("piece %d: proof hash %d differs from the seeder's", i, j)
			}
		}
		if err := m.VerifyPiece(i, data, proof); err != nil {
			t.Errorf("piece %d as the downloader serves it doesn't verify: %v", i, err)
		}
	}
}

func TestCorruptProviderIsSkipped(t *testing.T) {
	peers := startNetwork(t, 4)
	dir := t.TempDir()
	src := writeRandomFile(t, dir, 3*DefaultPieceSize)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	key, err := peers[0].peer.Share(ctx, src)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}

	// peers[1] claims the same file but serves a copy with every piece
	// corrupted, so whichever piece it's asked for fails verification.
	data, _ := os.ReadFile(src)
	for off := 7; off < len(data); off += DefaultPieceSize {
		data[off] ^= 0xFF
	}
	corrupt := filepath.Join(dir, "corrupt.bin")
	os.WriteFile(corrupt, data, 0o644)
	// It even sends the genuine Merkle proofs (the seeder's tree): only
	// the piece bytes are wrong, and the proof check must still catch it.
	m, mb, _ := peers[0].peer.lib.manifestFor(key)
	peers[1].peer.lib.addComplete(key, m, mb, corrupt, peers[0].peer.lib.files[key].tree)
	if err := peers[1].peer.announce(ctx, key); err != nil {
		t.Fatalf("announce: %v", err)
	}

	out := filepath.Join(dir, "out.bin")
	res, err := peers[2].peer.Download(ctx, key, out)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertSameFile(t, src, out)
	corruptAddr := ProviderRecord{IP: "127.0.0.1", Port: peers[1].peer.TCPPort()}.Addr()
	if len(res.FailedProviders) != 1 || res.FailedProviders[0] != corruptAddr {
		t.Errorf("expected the corrupt provider %s to be dropped, got %v", corruptAddr, res.FailedProviders)
	}
}

func TestDownloadUnknownKey(t *testing.T) {
	peers := startNetwork(t, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := filepath.Join(t.TempDir(), "never.bin")
	if _, err := peers[1].peer.Download(ctx, kademlia.RandomID(), out); err == nil {
		t.Fatal("expected an error for a key nobody shared")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a failed download left a file behind")
	}
}
