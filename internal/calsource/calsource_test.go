package calsource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/schedule"
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
	c := New([]Source{{Fetcher: &ICSFeed{URL: srv.URL, Client: srv.Client()}}}, ics.Options{Location: time.UTC})
	if _, err := c.Busy(from, to); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestUnionAndAllDayPerSource(t *testing.T) {
	srv := newServer(t)
	c := New([]Source{{Fetcher: &ICSFeed{URL: srv.URL, Client: srv.Client()}}, {Fetcher: &ICSFeed{URL: srv.URL + "/2", Client: srv.Client()}, AllDayBlocks: true}}, ics.Options{Location: time.UTC})
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
	c := New([]Source{{Fetcher: &ICSFeed{URL: srv.URL, Client: srv.Client()}}}, ics.Options{Location: time.UTC})
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
	c := New([]Source{{Fetcher: &ICSFeed{URL: srv.URL, Client: srv.Client()}}}, ics.Options{Location: time.UTC})
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

func TestStatus(t *testing.T) {
	srv := newServer(t)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	c := New([]Source{{Fetcher: &ICSFeed{URL: srv.URL + "/secret/path.ics", Client: srv.Client()}, AllDayBlocks: true}}, ics.Options{Location: time.UTC})
	c.now = func() time.Time { return now }
	if st := c.Status(); st[0].Current || !st[0].LastSuccess.IsZero() {
		t.Errorf("before fetch: %+v", st[0])
	}
	c.Refresh(context.Background())
	srv.fail.Store(true)
	c.Refresh(context.Background())
	st := c.Status()[0]
	if !st.Current || st.LastError == nil || !st.LastSuccess.Equal(now) || !st.AllDayBlocks || strings.Contains(st.Name, "secret") {
		t.Errorf("status %+v", st)
	}
}

type busySource struct {
	busy []schedule.Interval
	err  error
}

func (b *busySource) FetchBusy(context.Context) ([]schedule.Interval, error) { return b.busy, b.err }
func (b *busySource) Name() string                                           { return "Google Calendar" }

func TestBusySources(t *testing.T) {
	busy := []schedule.Interval{{Start: from.Add(time.Hour), End: from.Add(2 * time.Hour)}, {Start: to.Add(time.Hour), End: to.Add(2 * time.Hour)}}
	notConnected := &busySource{err: ErrNotConnected}
	c := New([]Source{{Busy: &busySource{busy: busy}}, {Busy: notConnected}}, ics.Options{Location: time.UTC})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("not-connected must not be a refresh error: %v", err)
	}
	got, err := c.Busy(from, to)
	if err != nil || len(got) != 1 {
		t.Fatalf("busy = %v, %v; want the one interval inside the window", got, err)
	}
	st := c.Status()[1]
	if !st.Current || !errors.Is(st.LastError, ErrNotConnected) || st.Name != "Google Calendar" {
		t.Errorf("status %+v", st)
	}
}
