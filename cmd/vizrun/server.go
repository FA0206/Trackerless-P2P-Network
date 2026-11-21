package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// routes is the runner's HTTP server: the page (viz/) and four
// endpoints of its own. The page is same-origin, so no CORS here.
//
//	GET  /viz/config      the nodes to watch and the run's settings
//	GET  /viz/status      SSE of runner facts (scene started/done, node up, windows, kills, hashes)
//	POST /viz/next        run the next scene (202; 409 while one runs)
//	POST /viz/subscribed  {"name": …}: the page has opened that node's stream
func (r *runner) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(r.cfg.webDir)))
	mux.HandleFunc("/viz/config", r.handleConfig)
	mux.HandleFunc("/viz/status", r.status.serve)
	mux.HandleFunc("/viz/next", r.handleNext)
	mux.HandleFunc("/viz/subscribed", r.handleSubscribed)
	return mux
}

// pageNode is one entry of /viz/config's node list. The page never
// assumes localhost: it only knows names and API addresses.
type pageNode struct {
	Name string `json:"name"`
	API  string `json:"api"`
	Dead bool   `json:"dead,omitempty"`
}

func (r *runner) handleConfig(w http.ResponseWriter, req *http.Request) {
	cfg := map[string]any{
		"k":            r.cfg.k,
		"alpha":        3, // cmd/node's default; vizrun doesn't change it
		"serve_delay":  r.cfg.serveDelay.String(),
		"max_inflight": r.cfg.maxInflight,
		"seeds":        fmt.Sprintf("%s0..%s%d", r.cfg.seedPrefix, r.cfg.seedPrefix, r.cfg.n-1),
		"commit":       r.commit,
		"scenes":       len(r.scenes),
		"watch":        r.cfg.watchFile != "",
	}
	if r.cfg.watchFile != "" {
		var watch struct {
			Nodes []pageNode `json:"nodes"`
		}
		data, err := os.ReadFile(r.cfg.watchFile)
		if err == nil {
			err = json.Unmarshal(data, &watch)
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read --watch file: " + err.Error()})
			return
		}
		cfg["nodes"] = watch.Nodes
	} else {
		nodes := []pageNode{}
		r.mu.Lock()
		for _, n := range r.nodes {
			nodes = append(nodes, pageNode{Name: n.Name, API: n.API, Dead: n.isDead()})
		}
		cfg["next_scene"] = r.next
		r.mu.Unlock()
		cfg["nodes"] = nodes
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (r *runner) handleNext(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if r.cfg.watchFile != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "watch mode: vizrun runs no scenes"})
		return
	}
	r.mu.Lock()
	busy, done := r.running, r.next >= len(r.scenes)
	r.mu.Unlock()
	switch {
	case busy:
		writeJSON(w, http.StatusConflict, map[string]string{"error": errBusy.Error()})
		return
	case done:
		writeJSON(w, http.StatusConflict, map[string]string{"error": errNoMoreScenes.Error()})
		return
	}
	// The scene outlives this request: the page only asked to start it.
	go r.runNext(context.Background())
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (r *runner) handleSubscribed(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if req.Method != http.MethodPost || json.NewDecoder(req.Body).Decode(&body) != nil {
		http.Error(w, "POST {\"name\": …}", http.StatusBadRequest)
		return
	}
	select {
	case r.pageAcks <- body.Name:
	default:
	}
	w.WriteHeader(http.StatusNoContent)
}

// hub fans runner facts out to every page watching /viz/status. Like the
// nodes' own event stream, it never waits for a slow viewer.
type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]bool
}

func newHub() *hub { return &hub{subs: map[chan []byte]bool{}} }

func (h *hub) publish(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- data:
		default:
		}
	}
}

func (h *hub) viewers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *hub) serve(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	ch := make(chan []byte, 256)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": vizrun status\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-req.Context().Done():
			return
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
		}
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // magnet links contain '&'
	enc.Encode(v)
}

// --- talking to nodes: plain HTTP, the same requests curl would make ---

var nodeClient = &http.Client{} // no client timeout: fetch blocks up to 100 s; contexts bound it

// getJSON reads one snapshot endpoint of a node into out.
func getJSON(ctx context.Context, api, path string, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+api+path, nil)
	if err != nil {
		return 0, err
	}
	res, err := nodeClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, fmt.Errorf("GET %s: HTTP %d", path, res.StatusCode)
	}
	return res.StatusCode, json.NewDecoder(res.Body).Decode(out)
}

// postJSON calls one control endpoint (--api-control). On a non-200
// answer it returns the node's {"error": …} text as the error.
func postJSON(ctx context.Context, api, path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+api+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := nodeClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("POST %s: HTTP %d", path, res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
