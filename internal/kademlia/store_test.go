package kademlia

import (
	"fmt"
	"testing"
)

func TestValueStoreKeepsDistinctValuesPerKey(t *testing.T) {
	s := newValueStore()
	key := HashID([]byte("file"))
	s.put(key, []byte("provider-a"))
	s.put(key, []byte("provider-b"))
	s.put(key, []byte("provider-a")) // duplicate: refreshed, not added twice

	got := s.get(key)
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct values, got %d: %q", len(got), got)
	}
	if string(got[0]) != "provider-b" || string(got[1]) != "provider-a" {
		t.Errorf("expected re-stored value to move to newest position, got %q", got)
	}
}

func TestValueStoreCapsValuesPerKey(t *testing.T) {
	s := newValueStore()
	key := HashID([]byte("file"))
	for i := 0; i < maxValuesPerKey+5; i++ {
		s.put(key, []byte(fmt.Sprintf("v%d", i)))
	}
	got := s.get(key)
	if len(got) != maxValuesPerKey {
		t.Fatalf("expected %d values, got %d", maxValuesPerKey, len(got))
	}
	if string(got[0]) != "v5" {
		t.Errorf("expected the oldest values to be dropped first, oldest kept is %q", got[0])
	}
}

func TestValueStoreGetReturnsCopies(t *testing.T) {
	s := newValueStore()
	key := HashID([]byte("k"))
	s.put(key, []byte("abc"))
	got := s.get(key)
	got[0][0] = 'X'
	if string(s.get(key)[0]) != "abc" {
		t.Error("mutating a returned value changed the store")
	}
}

// The limits in store.go are justified by "a full FIND_VALUE reply fits in
// one datagram". Check that claim against the real encoder.
func TestLargestFindValueReplyFitsInDatagram(t *testing.T) {
	values := make([][]byte, maxValuesPerKey)
	for i := range values {
		values[i] = make([]byte, maxValueSize)
	}
	env := Envelope{
		Type:     MsgFindValueResp,
		RPCID:    RandomID(),
		SenderID: RandomID(),
		Payload:  mustMarshal(FindValueResponse{Values: values}),
	}
	body := mustMarshal(env)
	if len(body)+4 > maxDatagramSize {
		t.Fatalf("largest FIND_VALUE reply is %d bytes, over the %d-byte datagram limit", len(body)+4, maxDatagramSize)
	}
}
