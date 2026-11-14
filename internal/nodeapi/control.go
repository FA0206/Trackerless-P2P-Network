package nodeapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"kademlia-demo/internal/kademlia"
)

// Control endpoints: POST /api/lookup, /ping, /put, /get, /share, /fetch.
// Each does what the console command of the same name does
// (cmd/node/console.go), through the same public Node / Peer methods,
// and answers with the result as JSON instead of printed text. They exist
// so the visual demo can trigger actions by clicking.
//
// They change things (share publishes a local file, fetch writes one), so
// they're registered only with Config.Control (cmd/node: --api-control).
// See docs/NODE-API.md, "Control endpoints", for what that exposes.

// maxRequestBody bounds a control request's JSON body. The largest
// legitimate one is a `put` with 256 bytes of text.
const maxRequestBody = 64 << 10

func (s *Server) registerControl() {
	s.mux.HandleFunc("POST /api/lookup", s.handleLookup)
	s.mux.HandleFunc("POST /api/ping", s.handlePing)
	s.mux.HandleFunc("POST /api/put", s.handlePut)
	s.mux.HandleFunc("POST /api/get", s.handleGet)
	s.mux.HandleFunc("POST /api/share", s.handleShare)
	s.mux.HandleFunc("POST /api/fetch", s.handleFetch)
}

// opTimeouts returns the deadlines for one DHT operation and for a whole
// download. Zero in Config means: the console's defaults (cmd/node).
func (s *Server) opTimeouts() (op, transfer time.Duration) {
	op = s.cfg.OpTimeout
	if op == 0 {
		op = max(10*time.Second, 5*s.cfg.RPCTimeout)
	}
	transfer = s.cfg.TransferTimeout
	if transfer == 0 {
		transfer = 10 * op
	}
	return op, transfer
}

// decodeRequest reads a JSON body into v. It insists on Content-Type
// application/json: a browser can't send that cross-site without a CORS
// preflight, so an HTML form on some other website can't trigger an
// action. Unknown fields are rejected, so a typo isn't silently ignored.
func decodeRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// failed reports an operation the node attempted but couldn't complete.
func failed(w http.ResponseWriter, what string, err error) {
	writeError(w, http.StatusInternalServerError, what+" failed: "+err.Error())
}

// ResultContact is a contact in a lookup or store result, with its XOR
// distance to the lookup's target (not to this node, unlike /api/table).
type ResultContact struct {
	ID       kademlia.ID `json:"id"`
	IP       string      `json:"ip"`
	Port     int         `json:"port"`
	Distance kademlia.ID `json:"distance"`
}

func resultContacts(cs []kademlia.Contact, target kademlia.ID) []ResultContact {
	out := make([]ResultContact, len(cs))
	for i, c := range cs {
		out[i] = ResultContact{ID: c.ID, IP: c.IP, Port: c.Port, Distance: kademlia.Xor(c.ID, target)}
	}
	return out
}

// --- lookup ---------------------------------------------------------------

// LookupRequest: {"target": "<40 hex>"} or {"target": "self"}.
type LookupRequest struct {
	Target string `json:"target"`
}

// LookupResponse is what the console's `lookup` prints, as JSON.
type LookupResponse struct {
	Target     kademlia.ID     `json:"target"`
	Rounds     int             `json:"rounds"`
	RPCsSent   int             `json:"rpcs_sent"`
	RPCsFailed int             `json:"rpcs_failed"`
	Closest    []ResultContact `json:"closest"` // nearest first
}

func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	var req LookupRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	target := s.cfg.Node.ID
	if req.Target != "self" {
		var err error
		if target, err = kademlia.IDFromHex(req.Target); err != nil {
			writeError(w, http.StatusBadRequest, `target must be 40 hex characters or "self"`)
			return
		}
	}
	op, _ := s.opTimeouts()
	ctx, cancel := context.WithTimeout(r.Context(), op)
	defer cancel()
	res, err := s.cfg.Node.Lookup(ctx, target)
	if err != nil {
		failed(w, "lookup", err)
		return
	}
	writeJSON(w, http.StatusOK, LookupResponse{Target: target, Rounds: res.Rounds, RPCsSent: res.RPCsSent,
		RPCsFailed: res.RPCsFailed, Closest: resultContacts(res.Closest, target)})
}

// --- ping -----------------------------------------------------------------

// PingRequest: {"addr": "host:port"}, a node's UDP address.
type PingRequest struct {
	Addr string `json:"addr"`
}

// PingResponse: whether a PONG came back within one RPC timeout.
type PingResponse struct {
	Addr  string      `json:"addr"`
	Alive bool        `json:"alive"`
	ID    kademlia.ID `json:"id,omitempty"` // who answered, from our routing table; only when alive
	RTTMS float64     `json:"rtt_ms,omitempty"`
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	var req PingRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	target, err := addrContact(req.Addr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RPCTimeout)
	defer cancel()
	start := time.Now()
	resp := PingResponse{Addr: req.Addr}
	if s.cfg.Node.Ping(ctx, target) {
		resp.Alive = true
		resp.RTTMS = float64(time.Since(start).Microseconds()) / 1000
		resp.ID = s.idAt(target.IP, target.Port)
	}
	writeJSON(w, http.StatusOK, resp)
}

// addrContact turns "host:port" into a Contact with an all-zero ID, like
// the console's ping: we only know the address; the PONG carries the real
// ID, which the node records in its routing table as usual.
func addrContact(addr string) (kademlia.Contact, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return kademlia.Contact{}, fmt.Errorf("addr must be host:port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return kademlia.Contact{}, fmt.Errorf("invalid port %q", portStr)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if net.ParseIP(host) == nil {
		return kademlia.Contact{}, fmt.Errorf("host must be an IP address, got %q", host)
	}
	return kademlia.Contact{IP: host, Port: port}, nil
}

// idAt finds the routing-table contact at ip:port, if any: after a
// successful PING the responder is in the table under its real ID.
func (s *Server) idAt(ip string, port int) kademlia.ID {
	for _, contacts := range s.cfg.Node.RoutingTableSnapshot() {
		for _, c := range contacts {
			if c.IP == ip && c.Port == port {
				return c.ID
			}
		}
	}
	return kademlia.ID{}
}

// --- put / get ------------------------------------------------------------

// PutRequest stores text under SHA-1(name), like the console's put.
type PutRequest struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// PutResponse: where the value ended up.
type PutResponse struct {
	Key           kademlia.ID     `json:"key"`
	StoredAt      []ResultContact `json:"stored_at"` // other nodes that acknowledged
	StoredLocally bool            `json:"stored_locally"`
	Failed        int             `json:"failed"`
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	var req PutRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	if req.Name == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "name and text are required")
		return
	}
	key := kademlia.HashID([]byte(req.Name))
	op, _ := s.opTimeouts()
	ctx, cancel := context.WithTimeout(r.Context(), op)
	defer cancel()
	res, err := s.cfg.Node.Store(ctx, key, []byte(req.Text))
	if err != nil {
		failed(w, "put", err)
		return
	}
	writeJSON(w, http.StatusOK, PutResponse{Key: key, StoredAt: resultContacts(res.StoredAt, key),
		StoredLocally: res.StoredLocally, Failed: res.Failed})
}

// GetRequest finds the values under SHA-1(name), like the console's get.
type GetRequest struct {
	Name string `json:"name"`
}

// GetResponse: the values found (empty if none) and how.
type GetResponse struct {
	Key            kademlia.ID     `json:"key"`
	Values         []StoredValue   `json:"values"`
	From           []ResultContact `json:"from"` // the nodes that returned them
	FromLocalStore bool            `json:"from_local_store"`
	Rounds         int             `json:"rounds"`
	RPCsSent       int             `json:"rpcs_sent"`
	RPCsFailed     int             `json:"rpcs_failed"`
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	var req GetRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	key := kademlia.HashID([]byte(req.Name))
	op, _ := s.opTimeouts()
	ctx, cancel := context.WithTimeout(r.Context(), op)
	defer cancel()
	res, err := s.cfg.Node.FindValue(ctx, key)
	if err != nil {
		failed(w, "get", err)
		return
	}
	out := GetResponse{Key: key, Values: make([]StoredValue, len(res.Values)), From: resultContacts(res.ValuesFrom, key),
		FromLocalStore: res.FromLocalStore, Rounds: res.Rounds, RPCsSent: res.RPCsSent, RPCsFailed: res.RPCsFailed}
	for i, v := range res.Values {
		out.Values[i] = storedValue(v)
	}
	writeJSON(w, http.StatusOK, out)
}

// storedValue gives a value as base64, and also as text when it's UTF-8.
func storedValue(v []byte) StoredValue {
	sv := StoredValue{Base64: base64.StdEncoding.EncodeToString(v)}
	if utf8.Valid(v) {
		text := string(v)
		sv.Text = &text
	}
	return sv
}

// --- share / fetch --------------------------------------------------------

// ShareRequest: {"path": "..."}, a file readable by the node's process.
// A relative path is relative to the node's working directory.
type ShareRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	var req ShareRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	op, _ := s.opTimeouts()
	ctx, cancel := context.WithTimeout(r.Context(), op)
	defer cancel()
	key, err := s.cfg.Peer.Share(ctx, req.Path)
	if err != nil {
		failed(w, "share", err)
		return
	}
	for _, f := range s.cfg.Peer.Library().List() {
		if f.Key == key {
			writeJSON(w, http.StatusOK, File{Key: f.Key, Name: f.Name, Size: f.Size, Pieces: f.Pieces, Path: f.Path})
			return
		}
	}
	writeJSON(w, http.StatusOK, File{Key: key, Path: req.Path}) // not reached: Share adds it first
}

// FetchRequest downloads the file with content key Key into Out, which
// must not exist yet (downloads never overwrite: O_EXCL, peer.go).
type FetchRequest struct {
	Key string `json:"key"`
	Out string `json:"out"`
}

// FetchResponse is what the console's fetch prints, as JSON.
type FetchResponse struct {
	Key             kademlia.ID    `json:"key"`
	Name            string         `json:"name"`
	Size            int64          `json:"size"`
	Pieces          int            `json:"pieces"`
	Out             string         `json:"out"`
	ProvidersFound  int            `json:"providers_found"`
	PiecesFrom      map[string]int `json:"pieces_from"` // provider TCP address -> pieces
	FailedProviders []string       `json:"failed_providers"`
	DurationMS      int64          `json:"duration_ms"`
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	var req FetchRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	key, err := kademlia.IDFromHex(req.Key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "key must be 40 hex characters")
		return
	}
	if req.Out == "" {
		writeError(w, http.StatusBadRequest, "out is required")
		return
	}
	_, transfer := s.opTimeouts()
	ctx, cancel := context.WithTimeout(r.Context(), transfer)
	defer cancel()
	res, err := s.cfg.Peer.Download(ctx, key, req.Out)
	if err != nil {
		failed(w, "fetch", err)
		return
	}
	failedProviders := res.FailedProviders
	if failedProviders == nil {
		failedProviders = []string{}
	}
	writeJSON(w, http.StatusOK, FetchResponse{Key: key, Name: res.Name, Size: res.Size, Pieces: res.Pieces, Out: req.Out,
		ProvidersFound: res.ProvidersFound, PiecesFrom: res.PiecesFrom, FailedProviders: failedProviders,
		DurationMS: res.Duration.Milliseconds()})
}

// allowPreflight answers a browser's CORS preflight (OPTIONS) for the
// control endpoints: a POST with Content-Type application/json from
// another origin is only sent after this says yes.
func allowPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions || !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(http.StatusNoContent)
	return true
}
