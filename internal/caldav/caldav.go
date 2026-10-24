// Package caldav connects hima to the Owner's CalDAV account (Radicale,
// Baïkal, Fastmail, iCloud, Nextcloud, …): reading calendars as a Calendar
// source, and writing Bookings into one calendar.
package caldav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// Account is one CalDAV login. Calendars are discovered from URL through the
// current-user-principal and calendar-home-set, and the result is cached.
type Account struct {
	URL      string
	Username string
	Password string
	HTTP     *http.Client

	mu        sync.Mutex
	client    *caldav.Client
	calendars []caldav.Calendar
}

func (a *Account) caldavClient() (*caldav.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		return a.client, nil
	}
	httpc := a.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	c, err := caldav.NewClient(webdav.HTTPClientWithBasicAuth(httpc, a.Username, a.Password), a.URL)
	if err != nil {
		return nil, err
	}
	a.client = c
	return c, nil
}

// Host is the account's server, for showing to the Owner.
func (a *Account) Host() string {
	if u, err := url.Parse(a.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return a.URL
}

// Calendars lists the account's event calendars, discovering them on first
// use or after forget.
func (a *Account) Calendars(ctx context.Context) ([]caldav.Calendar, error) {
	a.mu.Lock()
	cached := a.calendars
	a.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	c, err := a.caldavClient()
	if err != nil {
		return nil, err
	}
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("find principal: %w", err)
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("find calendar home: %w", err)
	}
	all, err := c.FindCalendars(ctx, home)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	var cals []caldav.Calendar
	for _, cal := range all {
		if len(cal.SupportedComponentSet) == 0 || slices.Contains(cal.SupportedComponentSet, "VEVENT") {
			cals = append(cals, cal)
		}
	}
	a.mu.Lock()
	a.calendars = cals
	a.mu.Unlock()
	return cals, nil
}

// forget drops cached discovery, e.g. after a calendar was renamed.
func (a *Account) forget() {
	a.mu.Lock()
	a.calendars = nil
	a.mu.Unlock()
}

// Find returns the calendar called name (its display name, or the last
// segment of its path), ignoring case.
func (a *Account) Find(ctx context.Context, name string) (caldav.Calendar, error) {
	cals, err := a.Calendars(ctx)
	if err != nil {
		return caldav.Calendar{}, err
	}
	for _, c := range cals {
		if strings.EqualFold(c.Name, name) || strings.EqualFold(strings.Trim(lastSegment(c.Path), "/"), name) {
			return c, nil
		}
	}
	a.forget()
	var names []string
	for _, c := range cals {
		names = append(names, displayName(c))
	}
	return caldav.Calendar{}, fmt.Errorf("no calendar named %q (found: %s)", name, strings.Join(names, ", "))
}

func lastSegment(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func displayName(c caldav.Calendar) string {
	if c.Name != "" {
		return c.Name
	}
	return lastSegment(c.Path)
}

// Source reads calendars of an Account as one Calendar source. It
// implements calsource.Fetcher.
type Source struct {
	Account *Account
	// Calendars are the names to read; nil reads every event calendar.
	Calendars []string
	// Horizon is how far ahead to read; past events are not needed.
	Horizon time.Duration
	Now     func() time.Time
}

func (s *Source) Name() string {
	if len(s.Calendars) == 0 {
		return "CalDAV " + s.Account.Host() + " (all calendars)"
	}
	return "CalDAV " + s.Account.Host() + " (" + strings.Join(s.Calendars, ", ") + ")"
}

func (s *Source) Fetch(ctx context.Context) ([]*ical.Calendar, error) {
	var cals []caldav.Calendar
	if s.Calendars == nil {
		all, err := s.Account.Calendars(ctx)
		if err != nil {
			return nil, err
		}
		cals = all
	} else {
		for _, name := range s.Calendars {
			c, err := s.Account.Find(ctx, name)
			if err != nil {
				return nil, err
			}
			cals = append(cals, c)
		}
	}
	client, err := s.Account.caldavClient()
	if err != nil {
		return nil, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	// A day of slack on both sides covers events that straddle the window.
	from, to := now().Add(-24*time.Hour), now().Add(s.Horizon+24*time.Hour)
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VEVENT", Start: from.UTC(), End: to.UTC()}},
		},
	}
	var out []*ical.Calendar
	for _, c := range cals {
		objs, err := client.QueryCalendar(ctx, c.Path, query)
		if err != nil {
			s.Account.forget()
			return nil, fmt.Errorf("%s: %w", displayName(c), err)
		}
		for _, o := range objs {
			if o.Data != nil {
				out = append(out, o.Data)
			}
		}
	}
	return out, nil
}

// Writer keeps Bookings mirrored into one calendar of an Account. Each
// Booking lives at a fixed path derived from its ID, so updates overwrite it
// and cancellations delete it without hima storing anything extra.
type Writer struct {
	Account  *Account
	Calendar string
}

func (w *Writer) objectPath(ctx context.Context, bookingID int64) (string, error) {
	if w.Calendar == "" {
		return "", fmt.Errorf("no write calendar configured")
	}
	cal, err := w.Account.Find(ctx, w.Calendar)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(cal.Path, "/") + fmt.Sprintf("/hima-booking-%d.ics", bookingID), nil
}

// Put creates or replaces the Booking's event.
func (w *Writer) Put(ctx context.Context, b store.Booking, ev ics.Event) error {
	p, err := w.objectPath(ctx, b.ID)
	if err != nil {
		return err
	}
	client, err := w.Account.caldavClient()
	if err != nil {
		return err
	}
	_, err = client.PutCalendarObject(ctx, p, ics.Build("", ev))
	return err
}

// Delete removes the Booking's event; a missing event is not an error.
func (w *Writer) Delete(ctx context.Context, b store.Booking, _ ics.Event) error {
	p, err := w.objectPath(ctx, b.ID)
	if err != nil {
		return err
	}
	client, err := w.Account.caldavClient()
	if err != nil {
		return err
	}
	err = client.RemoveAll(ctx, p)
	if isNotFound(err) {
		return nil
	}
	return err
}

// isNotFound recognises go-webdav's 404 error, whose type is internal; its
// message starts with the status line.
func isNotFound(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "404 ")
}
