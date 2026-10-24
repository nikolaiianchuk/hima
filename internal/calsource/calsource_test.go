package calsource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const feed = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:a\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20261026T090000Z\r\nDTEND:20261026T100000Z\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:b\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;VALUE=DATE:20261027\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

var (
	from = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	to   = time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
)

type flakyServer struct {
	*httptest.Server
	fail atomic.Bool
	hits atomic.Int32
}

func newServer(t *testing.T) *flakyServer {
	s := &flakyServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if s.fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Write([]byte(feed))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestBusyBeforeFirstFetchIsStale(t *testing.T) {
	srv := newServer(t)
	c := New([]Source{{URL: srv.URL}}, time.UTC, srv.Client())
	if _, err := c.Busy(from, to); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestUnionAndAllDayPerSource(t *testing.T) {
	srv := newServer(t)
	c := New([]Source{{URL: srv.URL}, {URL: srv.URL + "/2", AllDayBlocks: true}}, time.UTC, srv.Client())
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	busy, err := c.Busy(from, to)
	if err != nil {
		t.Fatal(err)
	}
	// Timed event from both sources, all-day event only from the second.
	if len(busy) != 3 {
		t.Errorf("got %d busy blocks, want 3: %v", len(busy), busy)
	}
}

func TestLastGoodCopyThenStale(t *testing.T) {
	srv := newServer(t)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	c := New([]Source{{URL: srv.URL}}, time.UTC, srv.Client())
	c.now = func() time.Time { return now }
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	srv.fail.Store(true)
	now = now.Add(30 * time.Minute)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	if _, err := c.Busy(from, to); err != nil {
		t.Fatalf("within MaxAge the last good copy should be used: %v", err)
	}

	now = now.Add(31 * time.Minute)
	if _, err := c.Busy(from, to); !errors.Is(err, ErrStale) {
		t.Fatalf("after MaxAge err = %v, want ErrStale", err)
	}
}

func TestRefreshIfOlder(t *testing.T) {
	srv := newServer(t)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	c := New([]Source{{URL: srv.URL}}, time.UTC, srv.Client())
	c.now = func() time.Time { return now }
	ctx := context.Background()

	c.RefreshIfOlder(ctx, 5*time.Minute)
	now = now.Add(time.Minute)
	c.RefreshIfOlder(ctx, 5*time.Minute)
	if got := srv.hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1 (fresh cache must not refetch)", got)
	}
	now = now.Add(5 * time.Minute)
	c.RefreshIfOlder(ctx, 5*time.Minute)
	if got := srv.hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2", got)
	}
}

func TestRedact(t *testing.T) {
	if got := redact("https://calendar.google.com/calendar/ical/secret/basic.ics"); got != "https://calendar.google.com/…" {
		t.Errorf("redact = %q", got)
	}
}
