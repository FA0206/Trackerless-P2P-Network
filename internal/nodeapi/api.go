// Package nodeapi serves one node's state as JSON over HTTP, so a browser
// (the visual demo) or curl can watch a running node without parsing its
// logs. It is optional: cmd/node starts it only with --api.
//
// It sits above the DHT and file transfer and uses only their public API
// (kademlia.Node, transfer.Peer); nothing in those packages knows it
// exists. See docs/NODE-API.md for every endpoint with real output.
package nodeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"kademlia-demo/internal/kademlia"
	"kademlia-demo/internal/transfer"
)

// Config is what the API serves. K, Alpha and RPCTimeout are passed in by
// the caller (cmd/node knows them from its flags) because kademlia.Node
// keeps them unexported and has no getters; adding getters to the DHT
// just for display wasn't worth a core change.
type Config struct {
	Node       *kademlia.Node
	Peer       *transfer.Peer
	K          int
	Alpha      int
	RPCTimeout time.Duration

	// Events feeds GET /api/events. It must be the hub whose TeeHandler
	// wraps the node's logger (cmd/node sets both up). Nil: no event
	// stream, and /api/events is 404.
	Events *Events

	// Control registers the POST endpoints (control.go) that make the
	// node act: lookup, ping, put, get, share, fetch. Off: they're 404.
	Control bool
	// OpTimeout bounds one DHT operation (lookup, put, get, share; a ping
	// gets one RPCTimeout) and TransferTimeout a whole fetch. Zero: the
	// console's defaults, max(10s, 5*RPCTimeout) and 10*OpTimeout.
	OpTimeout       time.Duration
	TransferTimeout time.Duration
}

// Server answers HTTP requests about one node.
type Server struct {
	cfg     Config
	started time.Time
	mux     *http.ServeMux
}

// New builds the API for one node. Serve its Handler with net/http.
func New(cfg Config) *Server {
	if cfg.RPCTimeout == 0 {
		cfg.RPCTimeout = kademlia.DefaultRPCTimeout // what kademlia.NewNode uses for 0, too
	}
	s := &Server{cfg: cfg, started: time.Now(), mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/node", s.handleNode)
	s.mux.HandleFunc("GET /api/table", s.handleTable)
	s.mux.HandleFunc("GET /api/store", s.handleStore)
	s.mux.HandleFunc("GET /api/files", s.handleFiles)
	if cfg.Events != nil {
		s.mux.HandleFunc("GET /api/events", s.handleEvents)
	}
	if cfg.Control {
		s.registerControl()
	}
	return s
}

// heartbeatInterval: an SSE comment sent when nothing else happened, so
// a client (or anything in between) can tell a quiet node from a dead
// connection.
const heartbeatInterval = 15 * time.Second

// handleEvents is GET /api/events[?level=debug|info|warn|error]: a
// Server-Sent Events stream, one event per log record at that level or
// above (default info). Each SSE message is
//
//	id: <seq>
//	data: <Event as JSON, plus "dropped">
//
// where "dropped" counts the events this viewer missed so far because it
// fell behind (see Events.publish). Browsers read it with EventSource.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	level := slog.LevelInfo
	if q := r.URL.Query().Get("level"); q != "" {
		if err := level.UnmarshalText([]byte(q)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "level must be debug, info, warn or error"})
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}
	sub, unsubscribe := s.cfg.Events.subscribe(level)
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, ": events from node %s, level %s and above\n\n", s.cfg.Node.ID, level)
	flusher.Flush()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done(): // the viewer disconnected, or the server is closing
			return
		case ev := <-sub.ch:
			err = writeEvent(w, ev, sub.dropped.Load())
		case <-heartbeat.C:
			_, err = fmt.Fprint(w, ": heartbeat\n\n")
		}
		if err != nil {
			return
		}
		flusher.Flush()
	}
}

func writeEvent(w io.Writer, ev Event, dropped uint64) error {
	data, err := json.Marshal(struct {
		Event
		Dropped uint64 `json:"dropped"`
	}{ev, dropped})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, data)
	return err
}

// Handler returns the API's HTTP handler. Every response allows any
// origin (CORS), so a visualizer page served from somewhere else (a file,
// another port) can read every node's API. That is safe only because the
// API listens on localhost by default; see docs/NODE-API.md.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if s.cfg.Control && allowPreflight(w, r) {
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

// NodeInfo is GET /api/node: who this node is and its settings.
type NodeInfo struct {
	ID               kademlia.ID `json:"id"`
	IP               string      `json:"ip"` // the advertised IP (--advertise-ip)
	UDPPort          int         `json:"udp_port"`
	TCPPort          int         `json:"tcp_port"`
	K                int         `json:"k"`
	Alpha            int         `json:"alpha"`
	RPCTimeoutMS     int64       `json:"rpc_timeout_ms"`
	UptimeMS         int64       `json:"uptime_ms"` // since the API started, just after the node
	RoutingTableSize int         `json:"routing_table_size"`
	StoredKeys       int         `json:"stored_keys"`
	Files            int         `json:"files"`
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	self := s.cfg.Node.Self()
	writeJSON(w, http.StatusOK, NodeInfo{
		ID:               self.ID,
		IP:               self.IP,
		UDPPort:          self.Port,
		TCPPort:          s.cfg.Peer.TCPPort(),
		K:                s.cfg.K,
		Alpha:            s.cfg.Alpha,
		RPCTimeoutMS:     s.cfg.RPCTimeout.Milliseconds(),
		UptimeMS:         time.Since(s.started).Milliseconds(),
		RoutingTableSize: s.cfg.Node.RoutingTableSize(),
		StoredKeys:       s.cfg.Node.StoredKeyCount(),
		Files:            len(s.cfg.Peer.Library().List()),
	})
}

// Table is GET /api/table: every non-empty k-bucket, farthest first (the
// same order as the console's `table`), contacts oldest first.
type Table struct {
	ID      kademlia.ID `json:"id"`
	K       int         `json:"k"`
	Size    int         `json:"size"`
	Buckets []Bucket    `json:"buckets"`
}

// Bucket is one k-bucket: index i holds contacts at XOR distance in
// [2^i, 2^(i+1)) from this node (docs/02-routing-table-and-kbuckets.md).
type Bucket struct {
	Index    int            `json:"index"`
	Contacts []TableContact `json:"contacts"`
}

// TableContact is one routing-table entry plus its XOR distance from this
// node, so a visualizer doesn't have to compute it.
type TableContact struct {
	ID       kademlia.ID `json:"id"`
	IP       string      `json:"ip"`
	Port     int         `json:"port"`
	Distance kademlia.ID `json:"distance"`
}

func (s *Server) handleTable(w http.ResponseWriter, r *http.Request) {
	self := s.cfg.Node.ID
	snapshot := s.cfg.Node.RoutingTableSnapshot()
	t := Table{ID: self, K: s.cfg.K, Buckets: []Bucket{}}
	for index, contacts := range snapshot {
		b := Bucket{Index: index, Contacts: make([]TableContact, len(contacts))}
		for i, c := range contacts {
			b.Contacts[i] = TableContact{ID: c.ID, IP: c.IP, Port: c.Port, Distance: kademlia.Xor(self, c.ID)}
		}
		t.Buckets = append(t.Buckets, b)
		t.Size += len(contacts)
	}
	sort.Slice(t.Buckets, func(i, j int) bool { return t.Buckets[i].Index > t.Buckets[j].Index })
	writeJSON(w, http.StatusOK, t)
}

// Store is GET /api/store: the values this node holds for the network
// (as one of the k closest nodes to each key), keys in ascending order.
type Store struct {
	Keys []StoredKey `json:"keys"`
}

// StoredKey is one key and its values, oldest first.
type StoredKey struct {
	Key    kademlia.ID   `json:"key"`
	Values []StoredValue `json:"values"`
}

// StoredValue is one value. The DHT stores opaque bytes, so it's always
// given as base64; when the bytes are valid UTF-8 (a `put` text, or a
// file's provider record, which is JSON) it's also given as text.
type StoredValue struct {
	Text   *string `json:"text,omitempty"`
	Base64 string  `json:"base64"`
}

func (s *Server) handleStore(w http.ResponseWriter, r *http.Request) {
	values := s.cfg.Node.StoredValues()
	out := Store{Keys: make([]StoredKey, 0, len(values))}
	for key, vals := range values {
		sk := StoredKey{Key: key, Values: make([]StoredValue, len(vals))}
		for i, v := range vals {
			sk.Values[i] = storedValue(v)
		}
		out.Keys = append(out.Keys, sk)
	}
	sort.Slice(out.Keys, func(i, j int) bool { return kademlia.Less(out.Keys[i].Key, out.Keys[j].Key) })
	writeJSON(w, http.StatusOK, out)
}

// Files is GET /api/files: the files this node can serve (shared or fully
// downloaded), sorted by name.
type Files struct {
	Files []File `json:"files"`
}

// File is one library entry. transfer.SharedFile has no JSON tags, so the
// API keeps its own shape rather than changing that type.
type File struct {
	Key    kademlia.ID `json:"key"`
	Name   string      `json:"name"`
	Size   int64       `json:"size"`
	Pieces int         `json:"pieces"`
	Path   string      `json:"path"`
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	list := s.cfg.Peer.Library().List()
	out := Files{Files: make([]File, len(list))}
	for i, f := range list {
		out.Files[i] = File{Key: f.Key, Name: f.Name, Size: f.Size, Pieces: f.Pieces, Path: f.Path}
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ") // readable with plain curl
	enc.Encode(v)           // the status is already sent; a write error means the client left
}
