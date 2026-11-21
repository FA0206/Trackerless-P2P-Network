package kademlia

import (
	"bytes"
	"sync"
	"time"
)

// Limits on what a node will hold for others. Both exist because a
// FIND_VALUE reply must fit in one UDP datagram (maxDatagramSize, 8 KB):
// values travel base64-encoded inside JSON (4/3 the size), so the worst
// case is 16 values * 256 B * 4/3 ~= 5.5 KB, leaving room for the envelope.
// That's plenty for small records like "node X has file Y" -- and it's why
// file data itself never goes through the DHT.
const (
	maxValueSize    = 256
	maxValuesPerKey = 16
)

// valueStore is a node's local share of the DHT: the (key, value) pairs
// other nodes asked it to keep via STORE.
//
// One key can hold several distinct values. The paper (Sec. 2.3) describes
// one value per key, but file sharing needs "every node that has file X"
// under one key, so storing the same key with a new value adds it rather
// than replacing (like BitTorrent's DHT, which keeps a list of peers per
// file). Storing an identical value again just marks it most recent.
//
// It also remembers when each key last got a STORE, so the republish loop
// (republish.go) can skip keys another node just republished (paper
// Sec. 2.5).
//
// SIMPLIFICATION: values never expire. The paper expires a value 24 h
// after publication unless its publisher publishes it again (Sec. 2.3), so
// records of nodes that left disappear. Doing that needs each STORE to
// carry the value's remaining lifetime, so a holder's republish doesn't
// extend it; that's a separate step. Here a value lives until the node
// holding it exits, and republishing (republish.go) keeps copying it.
type valueStore struct {
	mu        sync.Mutex
	values    map[ID][][]byte  // per key: oldest first
	refreshed map[ID]time.Time // per key: when the last put happened
	now       func() time.Time // time.Now; tests replace it
}

func newValueStore() *valueStore {
	return &valueStore{values: make(map[ID][][]byte), refreshed: make(map[ID]time.Time), now: time.Now}
}

// put adds value under key. Once a key holds maxValuesPerKey values, the
// oldest is dropped.
func (s *valueStore) put(key ID, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshed[key] = s.now()
	vals := s.values[key]
	for i, v := range vals {
		if bytes.Equal(v, value) {
			vals = append(vals[:i], vals[i+1:]...)
			break
		}
	}
	vals = append(vals, bytes.Clone(value))
	if len(vals) > maxValuesPerKey {
		vals = vals[len(vals)-maxValuesPerKey:]
	}
	s.values[key] = vals
}

// get returns copies of every value stored under key, oldest first.
func (s *valueStore) get(key ID) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.values[key]))
	for i, v := range s.values[key] {
		out[i] = bytes.Clone(v)
	}
	return out
}

// snapshot returns copies of every key's values, oldest first, for
// inspection (the per-node HTTP API). Copies, like get, so a caller can't
// change the store through the result.
func (s *valueStore) snapshot() map[ID][][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[ID][][]byte, len(s.values))
	for key, vals := range s.values {
		copies := make([][]byte, len(vals))
		for i, v := range vals {
			copies[i] = bytes.Clone(v)
		}
		out[key] = copies
	}
	return out
}

// notRefreshedSince returns copies of the values of every key whose last
// put is older than cutoff: the keys this holder should republish. A key
// that got a STORE after cutoff is left out, because whoever sent that
// STORE also sent it to the other k-1 closest nodes (paper Sec. 2.5).
func (s *valueStore) notRefreshedSince(cutoff time.Time) map[ID][][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[ID][][]byte)
	for key, vals := range s.values {
		if !s.refreshed[key].After(cutoff) {
			copies := make([][]byte, len(vals))
			for i, v := range vals {
				copies[i] = bytes.Clone(v)
			}
			out[key] = copies
		}
	}
	return out
}

// keyCount returns how many distinct keys this node holds values for.
func (s *valueStore) keyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.values)
}
