package caldav

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// memBackend is a tiny in-memory CalDAV server: one user, two calendars.
type memBackend struct {
	mu      sync.Mutex
	objects map[string]*ical.Calendar
}

var calendars = []caldav.Calendar{
	{Path: "/dav/me/cal/personal/", Name: "Personal", SupportedComponentSet: []string{"VEVENT"}},
	{Path: "/dav/me/cal/work/", Name: "Work", SupportedComponentSet: []string{"VEVENT"}},
	{Path: "/dav/me/cal/todo/", Name: "Tasks", SupportedComponentSet: []string{"VTODO"}},
}

func (b *memBackend) CurrentUserPrincipal(context.Context) (string, error) { return "/dav/me/", nil }
func (b *memBackend) CalendarHomeSetPath(context.Context) (string, error) {
	return "/dav/me/cal/", nil
}
func (b *memBackend) CreateCalendar(context.Context, *caldav.Calendar) error { return nil }
func (b *memBackend) ListCalendars(context.Context) ([]caldav.Calendar, error) {
	return calendars, nil
}
func (b *memBackend) GetCalendar(_ context.Context, path string) (*caldav.Calendar, error) {
	for _, c := range calendars {
		if c.Path == path {
			return &c, nil
		}
	}
	return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
}
func (b *memBackend) GetCalendarObject(_ context.Context, path string, _ *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cal, ok := b.objects[path]
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return &caldav.CalendarObject{Path: path, Data: cal, ETag: "x"}, nil
}
func (b *memBackend) ListCalendarObjects(_ context.Context, path string, _ *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []caldav.CalendarObject
	for p, cal := range b.objects {
		if strings.HasPrefix(p, path) {
			out = append(out, caldav.CalendarObject{Path: p, Data: cal, ETag: "x"})
		}
	}
	return out, nil
}
func (b *memBackend) QueryCalendarObjects(ctx context.Context, path string, q *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	all, _ := b.ListCalendarObjects(ctx, path, nil)
	return caldav.Filter(q, all)
}
func (b *memBackend) PutCalendarObject(_ context.Context, path string, cal *ical.Calendar, _ *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[path] = cal
	return &caldav.CalendarObject{Path: path, Data: cal, ETag: "x"}, nil
}
func (b *memBackend) DeleteCalendarObject(_ context.Context, path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.objects[path]; !ok {
		return webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	delete(b.objects, path)
	return nil
}

func (b *memBackend) has(path string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.objects[path]
	return ok
}

func event(uid, start, end string) *ical.Calendar {
	cal, err := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:" + start + "\r\nDTEND:" + end + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")).Decode()
	if err != nil {
		panic(err)
	}
	return cal
}

func newServer(t *testing.T) (*memBackend, *Account) {
	t.Helper()
	be := &memBackend{objects: map[string]*ical.Calendar{
		"/dav/me/cal/personal/a.ics":   event("a", "20261026T090000Z", "20261026T100000Z"),
		"/dav/me/cal/personal/old.ics": event("old", "20250101T090000Z", "20250101T100000Z"),
		"/dav/me/cal/work/b.ics":       event("b", "20261027T090000Z", "20261027T100000Z"),
	}}
	h := &caldav.Handler{Backend: be, Prefix: "/dav"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "app-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="caldav"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return be, &Account{URL: srv.URL + "/dav/", Username: "me", Password: "app-password", HTTP: srv.Client()}
}

var now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }

func uids(cals []*ical.Calendar) string {
	var out []string
	for _, c := range cals {
		for _, e := range c.Events() {
			uid, _ := e.Props.Text(ical.PropUID)
			out = append(out, uid)
		}
	}
	return strings.Join(out, ",")
}

func TestDiscoveryOnlyEventCalendars(t *testing.T) {
	_, acc := newServer(t)
	cals, err := acc.Calendars(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cals) != 2 {
		t.Fatalf("got %d calendars, want Personal and Work only: %+v", len(cals), cals)
	}
}

func TestSourceReadsNamedCalendarsInWindow(t *testing.T) {
	_, acc := newServer(t)
	src := &Source{Account: acc, Calendars: []string{"personal"}, Horizon: 30 * 24 * time.Hour, Now: now}
	cals, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := uids(cals); got != "a" {
		t.Errorf("uids = %q, want only a (Work not asked, old outside window)", got)
	}
	if !strings.Contains(src.Name(), "(personal)") || strings.Contains(src.Name(), "app-password") {
		t.Errorf("Name() = %q", src.Name())
	}

	all := &Source{Account: acc, Horizon: 30 * 24 * time.Hour, Now: now}
	cals, err = all.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := uids(cals); !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("all calendars: uids = %q", got)
	}
}

func TestUnknownCalendarNamesTheOptions(t *testing.T) {
	_, acc := newServer(t)
	_, err := (&Source{Account: acc, Calendars: []string{"Holidays"}, Now: now}).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Personal, Work") {
		t.Errorf("err = %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	_, acc := newServer(t)
	acc.Password = "nope"
	if _, err := acc.Calendars(context.Background()); err == nil {
		t.Error("expected an error")
	}
}

func TestWriterPutAndDelete(t *testing.T) {
	be, acc := newServer(t)
	w := &Writer{Account: acc, Calendar: "Personal"}
	ctx := context.Background()
	b := store.Booking{ID: 7}
	ev := ics.Event{UID: "booking-7@hima.example.com", Start: time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 26, 11, 30, 0, 0, time.UTC), Summary: "Alex", Stamp: now()}
	if err := w.Put(ctx, b, ev); err != nil {
		t.Fatal(err)
	}
	const path = "/dav/me/cal/personal/hima-booking-7.ics"
	if !be.has(path) {
		t.Fatal("event not written")
	}
	// Overwrite on reschedule.
	ev.Start, ev.End = ev.Start.AddDate(0, 0, 1), ev.End.AddDate(0, 0, 1)
	if err := w.Put(ctx, b, ev); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	ical.NewEncoder(&buf).Encode(be.objects[path])
	if !strings.Contains(buf.String(), "DTSTART:20261027T110000Z") {
		t.Errorf("not overwritten:\n%s", buf.String())
	}
	if err := w.Delete(ctx, b, ev); err != nil {
		t.Fatal(err)
	}
	if be.has(path) {
		t.Error("event not deleted")
	}
	if err := w.Delete(ctx, b, ev); err != nil {
		t.Errorf("deleting a missing event: %v", err)
	}
}
