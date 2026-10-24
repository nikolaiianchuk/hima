package writeback

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/nikolaiianchuk/hima/internal/notify"
	"github.com/nikolaiianchuk/hima/internal/store"
)

type fakeTarget struct {
	mu       sync.Mutex
	log      []string
	failures int
}

func (f *fakeTarget) Put(_ context.Context, id int64, _ *ical.Calendar) error {
	return f.record("put", id)
}

func (f *fakeTarget) Delete(_ context.Context, id int64) error {
	return f.record("delete", id)
}

func (f *fakeTarget) record(op string, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return errors.New("503 Service Unavailable")
	}
	f.log = append(f.log, op)
	return nil
}

type collect struct{ events []notify.Event }

func (c *collect) Notify(_ context.Context, ev notify.Event) { c.events = append(c.events, ev) }

func newMirror(t *testing.T, target *fakeTarget, n notify.Notifier) *Mirror {
	return &Mirror{
		Target:   target,
		Name:     "Personal (caldav.example.com)",
		Build:    func(store.Booking) *ical.Calendar { return ical.NewCalendar() },
		Notifier: n,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Backoff:  []time.Duration{time.Millisecond},
	}
}

func TestAppliesInOrderWithRetry(t *testing.T) {
	target := &fakeTarget{failures: 1}
	m := newMirror(t, target, nil)
	b := store.Booking{ID: 1, GuestName: "Alex"}
	m.Upsert(b)
	m.Upsert(b)
	m.Remove(b)
	m.Close(context.Background())
	if got := target.log; len(got) != 3 || got[0] != "put" || got[2] != "delete" {
		t.Errorf("log = %v", got)
	}
	if st := m.Status(); st.LastError != nil || st.LastSuccess.IsZero() {
		t.Errorf("status %+v", st)
	}
}

func TestReportsPersistentFailure(t *testing.T) {
	target := &fakeTarget{failures: 100}
	n := &collect{}
	m := newMirror(t, target, n)
	m.Remove(store.Booking{ID: 1, GuestName: "Alex"})
	m.Close(context.Background())
	if len(n.events) != 1 || n.events[0].Kind != notify.WriteBackFailed || n.events[0].Title != "Couldn't remove Alex's booking in your calendar" {
		t.Errorf("events %+v", n.events)
	}
	if m.Status().LastError == nil {
		t.Error("status should keep the error")
	}
}
