package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	l := New(3, 10*time.Minute)
	l.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("burst event %d refused", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("fourth event allowed")
	}
	if !l.Allow("b") {
		t.Fatal("other key limited")
	}
	now = now.Add(10 * time.Minute)
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("one token should refill per interval")
	}
	now = now.Add(time.Hour)
	l.Allow("c")
	if _, ok := l.buckets["b"]; ok {
		t.Error("idle bucket not swept")
	}
}
