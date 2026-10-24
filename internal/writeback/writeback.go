// Package writeback mirrors Bookings into the Owner's own calendar over
// CalDAV. Changes are applied in order by one background worker, retried,
// and reported to the Owner if they keep failing; they never block a Guest.
package writeback

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/notify"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// Target is where Bookings are written: a CalDAV, Google or Microsoft
// calendar.
type Target interface {
	Put(ctx context.Context, b store.Booking, ev ics.Event) error
	// Delete removes the Booking's event; ev identifies it (UID).
	Delete(ctx context.Context, b store.Booking, ev ics.Event) error
}

type job struct {
	booking store.Booking
	remove  bool
}

// Status is the latest outcome, for the admin.
type Status struct {
	Target      string
	LastSuccess time.Time
	LastError   error
}

type Mirror struct {
	Target Target
	// Name describes the target calendar for the Owner.
	Name string
	// Build turns a Booking into the event to write.
	Build func(store.Booking) ics.Event
	// AfterWrite, if set, runs after each successful write, e.g. to re-read
	// Calendar sources that would otherwise still show the old time.
	AfterWrite func()
	Notifier   notify.Notifier
	Log        *slog.Logger
	Backoff    []time.Duration
	Now        func() time.Time

	once sync.Once
	jobs chan job
	wg   sync.WaitGroup

	mu     sync.Mutex
	status Status
}

func (m *Mirror) start() {
	m.once.Do(func() {
		m.jobs = make(chan job, 256)
		m.status.Target = m.Name
		m.wg.Add(1)
		go m.run()
	})
}

// Upsert writes or rewrites b's event.
func (m *Mirror) Upsert(b store.Booking) { m.enqueue(job{booking: b}) }

// Remove deletes b's event.
func (m *Mirror) Remove(b store.Booking) { m.enqueue(job{booking: b, remove: true}) }

func (m *Mirror) enqueue(j job) {
	m.start()
	select {
	case m.jobs <- j:
	default:
		m.Log.Error("write-back queue full, dropping", "booking", j.booking.ID)
	}
}

// Close stops accepting work and waits for queued work, or until ctx ends.
func (m *Mirror) Close(ctx context.Context) {
	m.start()
	close(m.jobs)
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (m *Mirror) Status() Status {
	m.start()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Mirror) run() {
	defer m.wg.Done()
	for j := range m.jobs {
		err := m.apply(j)
		m.mu.Lock()
		if err == nil {
			m.status.LastSuccess, m.status.LastError = m.now(), nil
		} else {
			m.status.LastError = err
		}
		m.mu.Unlock()
		if err != nil {
			m.report(j, err)
		} else if m.AfterWrite != nil {
			m.AfterWrite()
		}
	}
}

func (m *Mirror) apply(j job) error {
	var err error
	for attempt := 0; attempt <= len(m.Backoff); attempt++ {
		if attempt > 0 {
			time.Sleep(m.Backoff[attempt-1])
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if j.remove {
			err = m.Target.Delete(ctx, j.booking, m.Build(j.booking))
		} else {
			err = m.Target.Put(ctx, j.booking, m.Build(j.booking))
		}
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

func (m *Mirror) report(j job, err error) {
	verb := "add"
	if j.remove {
		verb = "remove"
	}
	m.Log.Warn("write-back failed", "booking", j.booking.ID, "remove", j.remove, "err", err)
	if m.Notifier == nil {
		return
	}
	m.Notifier.Notify(context.Background(), notify.Event{
		Kind:    notify.WriteBackFailed,
		At:      m.now(),
		Title:   "Couldn't " + verb + " " + j.booking.GuestName + "'s booking in your calendar",
		Message: err.Error(),
	})
}

func (m *Mirror) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
