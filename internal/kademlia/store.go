package kademlia

import (
	"bytes"
	"sync"
)

// Limits on what a node will hold for others. Both exist because a
// FIND_VALUE reply must fit in one UDP datagram (maxDatagramSize, 8 KB):
// values travel base64-encoded inside JSON (4/3 the size), so the worst
// case is 16 values * 256 B * 4/3 ~= 5.5 KB, leaving room for the envelope.
// That's plenty for small records like "node X has file Y" -- and it's why
// file data itself never goes through the DHT (docs/06-store-and-find-value.md).
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
// SIMPLIFICATION: values never expire, and nobody republishes them. The
// paper re-publishes every hour and expires values after 24 h (Sec. 2.3,
// 2.5), which is how stale records (e.g. from a node that left) disappear
// and how records migrate to newly joined nodes that are closer to the
// key. Here a value lives until the node holding it exits.
type valueStore struct {
	mu     sync.Mutex
	values map[ID][][]byte // per key: oldest first
}

func newValueStore() *valueStore {
	return &valueStore{values: make(map[ID][][]byte)}
}

// put adds value under key. Once a key holds maxValuesPerKey values, the
// oldest is dropped.
func (s *valueStore) put(key ID, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// keyCount returns how many distinct keys this node holds values for.
func (s *valueStore) keyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.values)
}
