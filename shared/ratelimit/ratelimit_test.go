package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestPerKeyBuckets(t *testing.T) {
	l := New(1, 3)

	// The burst is available immediately, then the bucket is dry.
	for i := 0; i < 3; i++ {
		if !l.Allow("dev_a") {
			t.Fatalf("request %d within burst denied", i)
		}
	}
	if l.Allow("dev_a") {
		t.Fatal("request beyond burst allowed")
	}

	// Other keys have their own bucket.
	if !l.Allow("dev_b") {
		t.Fatal("independent key throttled")
	}
}

func TestIdleBucketsEvicted(t *testing.T) {
	l := New(1, 1)
	clock := time.Unix(0, 0)
	l.now = func() time.Time { return clock }

	for i := 0; i < sweepAt; i++ {
		l.Allow(fmt.Sprintf("key-%d", i))
	}
	if got := len(l.buckets); got != sweepAt {
		t.Fatalf("expected %d buckets before sweep, got %d", sweepAt, got)
	}

	// A key-rotating flood past the cap triggers a sweep once the existing
	// buckets have gone idle; the map must not keep growing.
	clock = clock.Add(maxIdle + time.Second)
	l.Allow("fresh-key")
	if got := len(l.buckets); got != 1 {
		t.Fatalf("expected idle buckets swept down to 1, got %d", got)
	}
}
