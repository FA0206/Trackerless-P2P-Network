// Command fileserver is the centralized baseline for graph 2
// (docs/10-performance-graphs.md): one plain HTTP server that every client
// downloads the whole file from. Written in Go with net/http so the
// comparison with the P2P swarm isn't skewed by a slower runtime.
//
// -rate caps the server's total upload the same way a node's
// --debug-upload-rate does (internal/transfer/server.go, uplink): one
// shared timeline for every connection, no burst. Same algorithm, so both
// sides of the comparison get an identical uplink.
package main

import (
	"flag"
	"log"
	"net/http"
	"sync"
	"time"
)

type uplink struct {
	mu   sync.Mutex
	rate int64 // bytes per second; 0 = unlimited
	next time.Time
}

func (u *uplink) wait(n int) {
	if u.rate <= 0 {
		return
	}
	u.mu.Lock()
	now := time.Now()
	if u.next.Before(now) {
		u.next = now
	}
	u.next = u.next.Add(time.Duration(int64(n) * int64(time.Second) / u.rate))
	d := u.next.Sub(now)
	u.mu.Unlock()
	time.Sleep(d)
}

// pacedWriter hides the ResponseWriter's ReadFrom, so every byte goes
// through Write and the uplink.
type pacedWriter struct {
	http.ResponseWriter
	up *uplink
}

func (w pacedWriter) Write(p []byte) (int, error) {
	w.up.wait(len(p))
	return w.ResponseWriter.Write(p)
}

func main() {
	dir := flag.String("dir", ".", "directory to serve")
	addr := flag.String("addr", "127.0.0.1:8080", "host:port to listen on")
	rate := flag.Int64("rate", 0, "cap total upload at this many bytes per second (0 = off)")
	flag.Parse()

	up := &uplink{rate: *rate}
	files := http.FileServer(http.Dir(*dir))
	log.Printf("serving %s on %s (rate %d B/s)", *dir, *addr, *rate)
	log.Fatal(http.ListenAndServe(*addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		files.ServeHTTP(pacedWriter{w, up}, r)
	})))
}
