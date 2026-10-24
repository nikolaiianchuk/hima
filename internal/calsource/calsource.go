// Package calsource fetches and caches the Owner's Calendar sources and
// answers which Busy blocks they contain.
//
// If any source cannot be trusted as current, Busy refuses to answer: showing
// a Slot that may be busy is worse than briefly showing none (see CONTEXT.md).
package calsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/emersion/go-ical"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/schedule"
)

// ErrStale means at least one Calendar source has no recent successful fetch.
var ErrStale = errors.New("calendar sources are not current")

const maxFeedSize = 20 << 20

// Fetcher reads one Calendar source's events.
type Fetcher interface {
	Fetch(ctx context.Context) ([]*ical.Calendar, error)
	// Name identifies the source for the Owner without revealing secrets.
	Name() string
}

// Source is one Calendar source.
type Source struct {
	Fetcher Fetcher
	// AllDayBlocks makes this source's all-day events Busy blocks.
	AllDayBlocks bool
}

// ICSFeed is a Calendar source published as an iCalendar URL.
type ICSFeed struct {
	URL    string
	Client *http.Client
}

func (f *ICSFeed) Name() string { return redact(f.URL) }

func (f *ICSFeed) Fetch(ctx context.Context) ([]*ical.Calendar, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/calendar")
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	cal, err := ical.NewDecoder(io.LimitReader(resp.Body, maxFeedSize)).Decode()
	if err != nil {
		return nil, fmt.Errorf("ics: %w", err)
	}
	return []*ical.Calendar{cal}, nil
}

type entry struct {
	cal       *ics.Calendar
	fetchedAt time.Time // last successful fetch
	err       error     // last fetch error, nil after a success
}

type Cache struct {
	sources []Source
	opts    ics.Options
	// MaxAge is how long the last good copy is trusted when fetches fail.
	MaxAge time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries []entry // parallel to sources
}

// New creates a Cache. opts describes the Owner (time zone, email addresses)
// to the feed parser.
func New(sources []Source, opts ics.Options) *Cache {
	return &Cache{
		sources: sources,
		opts:    opts,
		MaxAge:  time.Hour,
		now:     time.Now,
		entries: make([]entry, len(sources)),
	}
}

// Refresh fetches every source. A failed fetch keeps the last good copy; the
// returned error joins all failures.
func (c *Cache) Refresh(ctx context.Context) error {
	var (
		wg   sync.WaitGroup
		errs = make([]error, len(c.sources))
	)
	for i, src := range c.sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cals, err := src.Fetcher.Fetch(ctx)
			c.mu.Lock()
			defer c.mu.Unlock()
			if err != nil {
				c.entries[i].err = err
				errs[i] = fmt.Errorf("%s: %w", src.Fetcher.Name(), err)
				return
			}
			cal := ics.ParseCalendars(cals, c.opts)
			c.entries[i] = entry{cal: cal, fetchedAt: c.now()}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// RefreshIfOlder refreshes when the oldest successful fetch is older than
// age. Used right before confirming a Booking.
func (c *Cache) RefreshIfOlder(ctx context.Context, age time.Duration) error {
	if c.oldest().After(c.now().Add(-age)) {
		return nil
	}
	return c.Refresh(ctx)
}

// Run refreshes every interval until ctx is done, reporting each outcome
// (nil on success) to onResult, which may be nil.
func (c *Cache) Run(ctx context.Context, interval time.Duration, onResult func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		err := c.Refresh(ctx)
		if onResult != nil && ctx.Err() == nil {
			onResult(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Busy returns the union of Busy blocks overlapping [from, to) across all
// sources, or ErrStale if any source is not current.
func (c *Cache) Busy(from, to time.Time) ([]schedule.Interval, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := c.now().Add(-c.MaxAge)
	var out []schedule.Interval
	for i, e := range c.entries {
		if e.cal == nil || e.fetchedAt.Before(cutoff) {
			return nil, ErrStale
		}
		out = append(out, e.cal.Busy(from, to, c.sources[i].AllDayBlocks)...)
	}
	return out, nil
}

// SourceStatus describes one Calendar source for the admin.
type SourceStatus struct {
	// Name is the source's scheme and host; the secret path is hidden.
	Name         string
	AllDayBlocks bool
	// LastSuccess is the last successful fetch, zero if never.
	LastSuccess time.Time
	// LastError is the error of the latest fetch, nil if it succeeded.
	LastError error
	// Current reports whether the source is trusted for Slots right now.
	Current bool
}

// Status reports on every source, in configuration order.
func (c *Cache) Status() []SourceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := c.now().Add(-c.MaxAge)
	out := make([]SourceStatus, len(c.sources))
	for i, src := range c.sources {
		e := c.entries[i]
		out[i] = SourceStatus{
			Name:         src.Fetcher.Name(),
			AllDayBlocks: src.AllDayBlocks,
			LastSuccess:  e.fetchedAt,
			LastError:    e.err,
			Current:      e.cal != nil && !e.fetchedAt.Before(cutoff),
		}
	}
	return out
}

func (c *Cache) oldest() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	var oldest time.Time
	for i, e := range c.entries {
		if i == 0 || e.fetchedAt.Before(oldest) {
			oldest = e.fetchedAt
		}
	}
	return oldest
}

// redact hides the secret part of private feed URLs in logs and
// notifications, keeping only scheme and host.
func redact(raw string) string {
	for i := len("https://"); i < len(raw); i++ {
		if raw[i] == '/' {
			return raw[:i] + "/…"
		}
	}
	return raw
}
