package kademlia

import "testing"

func contactN(n byte) Contact {
	id := idFromByte(n)
	return Contact{ID: id, IP: "127.0.0.1", Port: 9000 + int(n)}
}

func TestBucketInsertUntilFull(t *testing.T) {
	b := NewBucket(2)

	outcome, _ := b.Update(contactN(1))
	if outcome != Seen {
		t.Fatalf("expected Seen on first insert, got %v", outcome)
	}
	outcome, _ = b.Update(contactN(2))
	if outcome != Seen {
		t.Fatalf("expected Seen on second insert, got %v", outcome)
	}
	if b.Len() != 2 {
		t.Fatalf("expected len 2, got %d", b.Len())
	}

	outcome, stale := b.Update(contactN(3))
	if outcome != FullNeedsPing {
		t.Fatalf("expected FullNeedsPing on third insert into capacity-2 bucket, got %v", outcome)
	}
	if stale.ID != contactN(1).ID {
		t.Fatalf("expected stale candidate to be the oldest (contact 1), got %s", stale)
	}
	if b.Len() != 2 {
		t.Fatalf("bucket should not have grown past capacity, got len %d", b.Len())
	}
}

func TestBucketReSeenMovesToTail(t *testing.T) {
	b := NewBucket(3)
	b.Update(contactN(1))
	b.Update(contactN(2))
	b.Update(contactN(3))

	// Re-seeing contact 1 should move it to the tail (most-recently-seen),
	// making contact 2 the new least-recently-seen / eviction candidate.
	b.Update(contactN(1))

	_, stale := b.Update(contactN(4)) // bucket full, triggers eviction check
	if stale.ID != contactN(2).ID {
		t.Fatalf("expected stale candidate to be contact 2 after re-seeing contact 1, got %s", stale)
	}
}

func TestBucketKeepAliveDropsNewContact(t *testing.T) {
	b := NewBucket(1)
	b.Update(contactN(1))
	_, stale := b.Update(contactN(2)) // bucket full

	b.KeepAlive(stale) // ping to contact 1 succeeded

	contacts := b.Contacts()
	if len(contacts) != 1 || contacts[0].ID != contactN(1).ID {
		t.Fatalf("expected bucket to still hold only contact 1, got %v", contacts)
	}
}

func TestBucketReplaceStaleEvicts(t *testing.T) {
	b := NewBucket(1)
	b.Update(contactN(1))
	_, stale := b.Update(contactN(2)) // bucket full

	ok := b.ReplaceStale(stale, contactN(2)) // ping to contact 1 failed
	if !ok {
		t.Fatal("expected ReplaceStale to succeed")
	}

	contacts := b.Contacts()
	if len(contacts) != 1 || contacts[0].ID != contactN(2).ID {
		t.Fatalf("expected bucket to now hold only contact 2, got %v", contacts)
	}
}

func TestBucketReplaceStaleNoOpIfAlreadyGone(t *testing.T) {
	b := NewBucket(1)
	b.Update(contactN(1))
	ok := b.ReplaceStale(contactN(99), contactN(2))
	if ok {
		t.Fatal("expected ReplaceStale to report false for a contact not in the bucket")
	}
	if b.Len() != 1 || b.Contacts()[0].ID != contactN(1).ID {
		t.Fatal("bucket contents should be unchanged")
	}
}

func TestBucketLiveNodesNeverDroppedOnNewArrival(t *testing.T) {
	// Paper Sec. 2.2: k-buckets never evict a contact just because a new
	// one showed up; eviction only happens after a failed ping.
	b := NewBucket(1)
	b.Update(contactN(1))
	for i := byte(2); i < 10; i++ {
		outcome, _ := b.Update(contactN(i))
		if outcome != FullNeedsPing {
			t.Fatalf("expected FullNeedsPing, got %v", outcome)
		}
		if b.Contacts()[0].ID != contactN(1).ID {
			t.Fatalf("contact 1 should remain until a ping to it actually fails")
		}
	}
}
