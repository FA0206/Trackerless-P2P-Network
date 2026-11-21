package nodeapi

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// The live event stream (GET /api/events) is fed by the node's own log
// records. The DHT and the file transfer already log every RPC sent and
// received, every value stored, every piece received, and so on, so
// tapping the logger gives a visualizer all of that without changing
// internal/kademlia or internal/transfer at all. The price: an event's
// name is its log message ("rpc sent"), so log text is part of the API's
// contract, as it already is for the demo scripts.

// Event is one log record, as the event stream sends it.
type Event struct {
	Seq   uint64         `json:"seq"`   // increases by one per event this node publishes
	Time  string         `json:"time"`  // RFC 3339, with nanoseconds
	Level string         `json:"level"` // DEBUG, INFO, WARN, ERROR
	Msg   string         `json:"msg"`   // the log message, e.g. "rpc sent"
	Attrs map[string]any `json:"attrs"` // the record's key=value pairs

	level slog.Level
}

// subscriberBuffer is how many events one viewer may fall behind before
// events are dropped for it. A localhost viewer keeps up easily; the
// limit exists so that a stalled one costs memory, never a blocked node.
const subscriberBuffer = 1024

// noSubscribers is the "lowest wanted level" when nobody is subscribed:
// higher than any real level, so no record is wanted.
const noSubscribers = math.MaxInt64

// Events fans log records out to the subscribers of GET /api/events.
//
// Rule: publishing never blocks. Log calls happen on the node's UDP
// handler goroutines (transport.go runs one per packet); if a viewer is
// slow, its events are dropped and counted instead.
type Events struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
	seq  uint64 // guarded by mu, so every subscriber sees seq in order

	// minLevel is the lowest level any subscriber wants, or noSubscribers.
	// Read without the lock on every log call (wants), so it's atomic.
	minLevel atomic.Int64
}

type subscriber struct {
	ch      chan Event
	min     slog.Level
	dropped atomic.Uint64
}

// NewEvents creates an event hub with no subscribers.
func NewEvents() *Events {
	e := &Events{subs: map[*subscriber]struct{}{}}
	e.minLevel.Store(noSubscribers)
	return e
}

// wants reports whether any subscriber wants records at this level.
func (e *Events) wants(level slog.Level) bool {
	return int64(level) >= e.minLevel.Load()
}

// subscribe registers a viewer for events at min level and above. Call
// the returned function to unsubscribe.
func (e *Events) subscribe(min slog.Level) (*subscriber, func()) {
	s := &subscriber{ch: make(chan Event, subscriberBuffer), min: min}
	e.mu.Lock()
	e.subs[s] = struct{}{}
	e.recomputeMinLevel()
	e.mu.Unlock()
	return s, func() {
		e.mu.Lock()
		delete(e.subs, s)
		e.recomputeMinLevel()
		e.mu.Unlock()
	}
}

// recomputeMinLevel must be called with mu held.
func (e *Events) recomputeMinLevel() {
	min := int64(noSubscribers)
	for s := range e.subs {
		min = int64(minLevel(slog.Level(min), s.min))
	}
	e.minLevel.Store(min)
}

func minLevel(a, b slog.Level) slog.Level {
	if a < b {
		return a
	}
	return b
}

// subscriberCount is for tests.
func (e *Events) subscriberCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.subs)
}

// publish numbers ev and offers it to every subscriber that wants its
// level, without ever waiting: a full subscriber gets a drop instead.
func (e *Events) publish(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	ev.Seq = e.seq
	for s := range e.subs {
		if ev.level < s.min {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			s.dropped.Add(1)
		}
	}
}

// TeeHandler wraps a node's log handler so every record also becomes an
// event. The wrapped handler gets exactly the records it would get on its
// own (it decides by its own Enabled), so the node's printed output is
// byte-for-byte unchanged. Records below the wrapped handler's level
// (e.g. DEBUG without --verbose) are built only while some subscriber
// asked for them, so an API nobody is watching costs nothing.
func TeeHandler(inner slog.Handler, events *Events) slog.Handler {
	return &teeHandler{inner: inner, events: events}
}

type teeHandler struct {
	inner  slog.Handler
	events *Events
	attrs  []slog.Attr // from WithAttrs, keys already prefixed with groups
	group  string      // from WithGroup, "a.b." style prefix
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level) || h.events.wants(level)
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.inner.Enabled(ctx, r.Level) {
		err = h.inner.Handle(ctx, r)
	}
	if h.events.wants(r.Level) {
		h.events.publish(h.toEvent(r))
	}
	return err
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.inner = h.inner.WithAttrs(attrs)
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	for _, a := range attrs {
		c.attrs = append(c.attrs, slog.Attr{Key: h.group + a.Key, Value: a.Value})
	}
	return &c
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.inner = h.inner.WithGroup(name)
	c.group = h.group + name + "."
	return &c
}

func (h *teeHandler) toEvent(r slog.Record) Event {
	ev := Event{
		Time:  r.Time.Format(time.RFC3339Nano),
		Level: r.Level.String(),
		Msg:   r.Message,
		Attrs: map[string]any{},
		level: r.Level,
	}
	for _, a := range h.attrs {
		ev.Attrs[a.Key] = attrValue(a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		ev.Attrs[h.group+a.Key] = attrValue(a.Value)
		return true
	})
	return ev
}

// attrValue turns a log value into something JSON shows naturally:
// numbers and booleans stay numbers and booleans, durations become
// strings like "2.004s", and everything else (strings, IDs, errors)
// becomes its string form.
func attrValue(v slog.Value) any {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	default: // string, duration, group, any (errors, Stringers)
		return v.String()
	}
}
