package web

import (
	"context"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/nikolaiianchuk/hima/internal/booking"
	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/notify"
	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
)

var lisbon, _ = time.LoadLocation("Europe/Lisbon")

type fakeCalendars struct{ stale bool }

func (f *fakeCalendars) Busy(from, to time.Time) ([]schedule.Interval, error) {
	if f.stale {
		return nil, calsource.ErrStale
	}
	return nil, nil
}

func (f *fakeCalendars) RefreshIfOlder(context.Context, time.Duration) error { return nil }

type testServer struct {
	*httptest.Server
	cals *fakeCalendars
	c    *http.Client
}

// newTestServer runs on Sunday 2026-10-25 09:00 Lisbon with Availability
// Mon–Fri 10:00–12:00.
func newTestServer(t *testing.T, auth config.Auth) *testServer {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hima.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var weekly [7][]schedule.DayRange
	for d := time.Monday; d <= time.Friday; d++ {
		weekly[d] = []schedule.DayRange{{From: 10 * 60, To: 12 * 60}}
	}
	cals := &fakeCalendars{}
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, lisbon)
	svc := &booking.Service{
		Store:        st,
		Availability: schedule.Availability{Location: lisbon, Weekly: weekly, Horizon: 14 * 24 * time.Hour, Step: 30 * time.Minute},
		Calendars:    cals,
		Now:          func() time.Time { return now },
		Notifier:     &notify.Dispatcher{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		OwnerName:    "Nikolai",
		UIDDomain:    "hima.example.com",
	}
	cfg := &config.Config{Owner: config.Owner{Name: "Nikolai"}, Auth: auth}
	cfg.InviteDefaults = config.InviteDefaults{ExpiresInDays: 7, MaxUses: 1, Durations: []time.Duration{30 * time.Minute}}
	ts := httptest.NewServer(New(cfg, st, svc, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ts.Close)
	return &testServer{
		Server: ts,
		cals:   cals,
		c:      &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (ts *testServer) get(t *testing.T, path string) (int, string, http.Header) {
	t.Helper()
	resp, err := ts.c.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func (ts *testServer) post(t *testing.T, path string, form url.Values) (int, string, http.Header) {
	t.Helper()
	resp, err := ts.c.PostForm(ts.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func mustContain(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("body does not contain %q:\n%s", w, body)
		}
	}
}

func first(t *testing.T, re *regexp.Regexp, body string) string {
	t.Helper()
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no match for %s in:\n%s", re, body)
	}
	return html.UnescapeString(m[1])
}

var (
	inviteLinkRe = regexp.MustCompile(`value="https?://[^"/]+(/i/[A-Za-z0-9_-]{22})"`)
	slotLinkRe   = regexp.MustCompile(`href="(/[ib]/[^"]+start=\d+[^"]*)"`)
	startRe      = regexp.MustCompile(`name="start" value="(\d+)"`)
)

func TestGuestFlow(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})

	// Owner creates an Invite.
	status, body, _ := ts.post(t, "/admin/invites", url.Values{
		"label": {"for Alex"}, "durations": {"30"}, "max_uses": {"1"}, "expires_in_days": {"7"}, "location": {"Café Nicola"},
	})
	if status != http.StatusOK {
		t.Fatalf("create invite: %d\n%s", status, body)
	}
	invite := first(t, inviteLinkRe, body)

	// Guest sees Slots in their zone: Monday 10:00 Lisbon is 06:00 in New York.
	_, body, _ = ts.get(t, invite+"?tz=America/New_York")
	mustContain(t, body, "Book time with Nikolai", "Café Nicola", "Mon 26 Oct", ">06:00<")
	slot := first(t, slotLinkRe, body)

	_, body, _ = ts.get(t, slot)
	mustContain(t, body, "Confirm your time", "Mon 26 Oct, 06:00–06:30", "10:00–10:30 in Nikolai&#39;s time")
	start := first(t, startRe, body)

	// Missing name is refused with a message.
	status, body, _ = ts.post(t, invite, url.Values{"start": {start}, "d": {"30"}, "tz": {"America/New_York"}})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("no name: status %d", status)
	}
	mustContain(t, body, "Please tell us your name.")

	status, _, h := ts.post(t, invite, url.Values{"start": {start}, "d": {"30"}, "tz": {"America/New_York"}, "name": {"Alex"}, "note": {"trip"}})
	if status != http.StatusSeeOther {
		t.Fatalf("book: status %d", status)
	}
	manage := h.Get("Location")
	managePath := strings.SplitN(manage, "?", 2)[0]

	_, body, _ = ts.get(t, manage)
	mustContain(t, body, "You're booked", "Alex ↔ Nikolai", "Save this link", managePath, "Confirmed with Nikolai")

	_, body, h = ts.get(t, managePath+"/event.ics")
	if !strings.HasPrefix(h.Get("Content-Type"), "text/calendar") {
		t.Fatalf("ics content type %q", h.Get("Content-Type"))
	}
	mustContain(t, body, "BEGIN:VEVENT", "UID:booking-1@hima.example.com", "SUMMARY:Alex ↔ Nikolai: trip", "LOCATION:Café Nicola", "DTSTART:20261026T100000Z", "SEQUENCE:0")

	// One use: the Invite is dead now.
	status, body, _ = ts.get(t, invite)
	if status != http.StatusNotFound {
		t.Fatalf("used-up invite: status %d", status)
	}
	mustContain(t, body, "Ask Nikolai to send you a new one")

	// Reschedule to another Slot.
	_, body, _ = ts.get(t, managePath+"/reschedule?tz=Europe/Lisbon")
	mustContain(t, body, "Pick a new time", "Currently Mon 26 Oct, 10:00–10:30")
	slot = first(t, slotLinkRe, body)
	_, body, _ = ts.get(t, slot)
	mustContain(t, body, "Move your booking here?")
	status, _, _ = ts.post(t, managePath+"/reschedule", url.Values{"start": {first(t, startRe, body)}, "tz": {"Europe/Lisbon"}})
	if status != http.StatusSeeOther {
		t.Fatalf("reschedule: status %d", status)
	}
	_, body, _ = ts.get(t, managePath+"/event.ics")
	mustContain(t, body, "SEQUENCE:1")

	// Cancel.
	status, _, _ = ts.post(t, managePath+"/cancel", nil)
	if status != http.StatusSeeOther {
		t.Fatalf("cancel: status %d", status)
	}
	_, body, _ = ts.get(t, managePath+"?tz=Europe/Lisbon")
	mustContain(t, body, "Cancelled by you.")
	if strings.Contains(body, "Cancel booking") {
		t.Error("cancelled booking still offers cancel")
	}

	// The Owner sees it.
	_, body, _ = ts.get(t, "/admin/")
	mustContain(t, body, "Alex", "Cancelled by guest", "used up")
}

func TestOwnerCancelsAndRevokes(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, body, _ := ts.post(t, "/admin/invites", url.Values{"durations": {"30"}, "max_uses": {"3"}, "expires_in_days": {"7"}})
	invite := first(t, inviteLinkRe, body)
	_, body, _ = ts.get(t, invite+"?tz=Europe/Lisbon")
	_, body, _ = ts.get(t, first(t, slotLinkRe, body))
	_, _, h := ts.post(t, invite, url.Values{"start": {first(t, startRe, body)}, "d": {"30"}, "tz": {"Europe/Lisbon"}, "name": {"Maria"}})
	manage := strings.SplitN(h.Get("Location"), "?", 2)[0]

	if status, _, _ := ts.post(t, "/admin/bookings/1/cancel", nil); status != http.StatusSeeOther {
		t.Fatalf("owner cancel: %d", status)
	}
	_, body, _ = ts.get(t, manage+"?tz=Europe/Lisbon")
	mustContain(t, body, "Cancelled by Nikolai.")

	if status, _, _ := ts.post(t, "/admin/invites/1/revoke", nil); status != http.StatusSeeOther {
		t.Fatalf("revoke: %d", status)
	}
	if status, _, _ := ts.get(t, invite); status != http.StatusNotFound {
		t.Errorf("revoked invite: status %d", status)
	}
}

func TestInviteFormErrors(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	status, body, _ := ts.post(t, "/admin/invites", url.Values{"label": {"oops"}, "max_uses": {"1"}, "expires_in_days": {"7"}})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", status)
	}
	mustContain(t, body, "Pick at least one duration.", `value="oops"`)
}

func TestStaleCalendarsShowUnavailable(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, body, _ := ts.post(t, "/admin/invites", url.Values{"durations": {"30"}, "max_uses": {"1"}, "expires_in_days": {"7"}})
	invite := first(t, inviteLinkRe, body)
	ts.cals.stale = true
	status, body, _ := ts.get(t, invite)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status %d", status)
	}
	mustContain(t, body, "temporarily unavailable")
	_, body, _ = ts.get(t, "/admin/")
	mustContain(t, body, "guests currently see no free times")
}

func TestCrossSitePostRejected(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	req, _ := http.NewRequest("POST", ts.URL+"/admin/invites", strings.NewReader("durations=30&max_uses=1&expires_in_days=7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := ts.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
}

func TestTokenPagesAreNotCachedOrLeaked(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, _, h := ts.get(t, "/i/whatever")
	if h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers: %v", h)
	}
}

func TestProxyModeRequiresHeaderWhenConfigured(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy, ProxyHeader: "Remote-User"})
	if status, _, _ := ts.get(t, "/admin/"); status != http.StatusUnauthorized {
		t.Errorf("without header: status = %d, want 401", status)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/admin/", nil)
	req.Header.Set("Remote-User", "nikolai")
	resp, err := ts.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("with header: status = %d, want 200", resp.StatusCode)
	}
}

func TestPasswordModeLoginFlow(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	ts := newTestServer(t, config.Auth{Mode: config.AuthModePassword, PasswordHash: string(hash)})

	status, _, h := ts.get(t, "/admin/")
	if status != http.StatusSeeOther || h.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous admin: status = %d, location = %q", status, h.Get("Location"))
	}
	if status, _, _ := ts.post(t, "/admin/login", url.Values{"password": {"wrong"}}); status != http.StatusUnauthorized {
		t.Errorf("wrong password: status = %d, want 401", status)
	}
	resp, err := ts.c.PostForm(ts.URL+"/admin/login", url.Values{"password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cookies := resp.Cookies()
	if resp.StatusCode != http.StatusSeeOther || len(cookies) == 0 {
		t.Fatalf("login: status = %d, cookies = %d", resp.StatusCode, len(cookies))
	}
	req, _ := http.NewRequest("GET", ts.URL+"/admin/", nil)
	req.AddCookie(cookies[0])
	resp, err = ts.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Hi, Nikolai") {
		t.Errorf("logged-in admin: status = %d", resp.StatusCode)
	}
}

func TestDeadInviteIsNeutral(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	status, body, _ := ts.get(t, "/i/whatever")
	if status != http.StatusNotFound {
		t.Errorf("status = %d", status)
	}
	mustContain(t, body, "Ask Nikolai to send you a new one")
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{"/i/abc": "/i/…", "/b/abc/cancel": "/b/…/cancel", "/admin/": "/admin/"} {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, want %q", in, got, want)
		}
	}
}

var feedLinkRe = regexp.MustCompile(`value="https?://[^"/]+(/feed/[A-Za-z0-9_-]{22}\.ics)"`)

func (ts *testServer) book(t *testing.T, name string) string {
	t.Helper()
	_, body, _ := ts.post(t, "/admin/invites", url.Values{"label": {"for " + name}, "durations": {"30"}, "max_uses": {"1"}, "expires_in_days": {"7"}})
	invite := first(t, inviteLinkRe, body)
	_, body, _ = ts.get(t, invite+"?tz=Europe/Lisbon")
	_, body, _ = ts.get(t, first(t, slotLinkRe, body))
	_, _, h := ts.post(t, invite, url.Values{"start": {first(t, startRe, body)}, "d": {"30"}, "tz": {"Europe/Lisbon"}, "name": {name}, "note": {"hello"}})
	return strings.SplitN(h.Get("Location"), "?", 2)[0]
}

func TestOwnerFeed(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, body, _ := ts.get(t, "/admin/")
	mustContain(t, body, "Create feed link")

	manage := ts.book(t, "Alex")
	ts.post(t, manage+"/cancel", nil)
	ts.book(t, "Maria")

	_, body, _ = ts.post(t, "/admin/feed", nil)
	feed := first(t, feedLinkRe, body)

	status, body, h := ts.get(t, feed)
	if status != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "text/calendar") {
		t.Fatalf("feed: %d %s", status, h.Get("Content-Type"))
	}
	mustContain(t, body, "X-WR-CALNAME:hima: bookings with Nikolai", "REFRESH-INTERVAL;VALUE=DURATION:PT15M",
		"SUMMARY:Alex ↔ Nikolai: hello", "STATUS:CANCELLED", "SUMMARY:Maria ↔ Nikolai: hello", "Booked via invite: for Maria")

	// Without the .ics suffix works too; a wrong token does not.
	if status, _, _ := ts.get(t, strings.TrimSuffix(feed, ".ics")); status != http.StatusOK {
		t.Errorf("feed without suffix: %d", status)
	}
	if status, _, _ := ts.get(t, "/feed/AAAAAAAAAAAAAAAAAAAAAA.ics"); status != http.StatusNotFound {
		t.Errorf("wrong token: %d", status)
	}

	// A new link invalidates the old one.
	_, body, _ = ts.post(t, "/admin/feed", nil)
	if status, _, _ := ts.get(t, feed); status != http.StatusNotFound {
		t.Errorf("old feed after rotation: %d", status)
	}
	if status, _, _ := ts.get(t, first(t, feedLinkRe, body)); status != http.StatusOK {
		t.Errorf("new feed: %d", status)
	}
	_, body, _ = ts.get(t, "/admin/")
	mustContain(t, body, "feed is active")
}

func TestActivityLog(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	manage := ts.book(t, "Alex")
	ts.post(t, manage+"/cancel", nil)
	_, body, _ := ts.get(t, "/admin/")
	mustContain(t, body, "New booking: Alex", "Alex cancelled")
}
