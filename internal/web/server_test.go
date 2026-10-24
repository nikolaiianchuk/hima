package web

import (
	"context"
	"errors"
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

type fakeCalendars struct {
	stale bool
	busy  []schedule.Interval
	now   time.Time
}

func (f *fakeCalendars) Busy(from, to time.Time) ([]schedule.Interval, error) {
	if f.stale {
		return nil, calsource.ErrStale
	}
	return f.busy, nil
}

func (f *fakeCalendars) RefreshIfOlder(context.Context, time.Duration) error { return nil }

func (f *fakeCalendars) Status() []calsource.SourceStatus {
	st := calsource.SourceStatus{Name: "https://calendar.example.com/…", Current: !f.stale, LastSuccess: f.now.Add(-3 * time.Minute)}
	if f.stale {
		st.LastSuccess = f.now.Add(-72 * time.Minute)
		st.LastError = errors.New("HTTP 502 Bad Gateway")
	}
	return []calsource.SourceStatus{st}
}

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
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, lisbon)
	cals := &fakeCalendars{now: now}
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
	feedLinkRe   = regexp.MustCompile(`value="https?://[^"/]+(/feed/[A-Za-z0-9_-]{22}\.ics)"`)
	slotLinkRe   = regexp.MustCompile(`class="slot" href="(/[ib]/[^"]+start=\d+[^"]*)"`)
	dayLinkRe    = regexp.MustCompile(`class="day" href="([^"]+)" aria-label="([^"]+)"`)
	startRe      = regexp.MustCompile(`name="start" value="(\d+)"`)
)

var defaultInvite = url.Values{"durations": {"30"}, "max_uses": {"1"}, "expires_in_days": {"7"}, "location_kind": {"place"}}

func (ts *testServer) createInvite(t *testing.T, form url.Values) string {
	t.Helper()
	status, body, _ := ts.post(t, "/admin/invites", form)
	if status != http.StatusOK {
		t.Fatalf("create invite: %d\n%s", status, body)
	}
	return first(t, inviteLinkRe, body)
}

func (ts *testServer) book(t *testing.T, name string) string {
	t.Helper()
	form := cloneValues(defaultInvite)
	form.Set("label", "for "+name)
	invite := ts.createInvite(t, form)
	_, body, _ := ts.get(t, invite+"?tz=Europe/Lisbon")
	_, body, _ = ts.get(t, first(t, slotLinkRe, body))
	_, _, h := ts.post(t, invite, url.Values{"start": {first(t, startRe, body)}, "d": {"30"}, "tz": {"Europe/Lisbon"}, "name": {name}, "note": {"hello"}})
	return strings.SplitN(h.Get("Location"), "?", 2)[0]
}

func TestGuestFlow(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})

	form := cloneValues(defaultInvite)
	form.Set("label", "Alex")
	form.Set("location", "Café Nicola")
	invite := ts.createInvite(t, form)

	// Slots in the Guest's zone: Monday 10:00 Lisbon is 06:00 in New York.
	_, body, _ := ts.get(t, invite+"?tz=America/New_York")
	mustContain(t, body, "Book time with Nikolai", "Café Nicola", "Mon 26 October", ">06:00<", "New York (UTC-4)", "4 times")
	slot := first(t, slotLinkRe, body)

	_, body, _ = ts.get(t, slot)
	mustContain(t, body, "Almost done", "Monday 26 October", "06:00 – 06:30", "10:00 – 10:30 in Nikolai&#39;s time",
		`value="Alex"`, "Book 06:00 – 06:30", "Other times")
	start := first(t, startRe, body)

	// Missing name is refused with a message.
	status, body, _ := ts.post(t, invite, url.Values{"start": {start}, "d": {"30"}, "tz": {"America/New_York"}})
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
	mustContain(t, body, "You're booked, Alex", "Monday 26 October · with Nikolai", "06:00 – 06:30", "UTC-4", "Keep this link", managePath, "Add to calendar (.ics)")

	_, body, _ = ts.get(t, managePath+"?tz=America/New_York")
	mustContain(t, body, "Your Booking with Nikolai", "Upcoming · tomorrow", "Café Nicola", "trip", "Pick another time", "Cancel this Booking")

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
	mustContain(t, body, "This Invite can&#39;t be used", "Ask the person who sent it for a new one.")

	// Reschedule to another Slot.
	_, body, _ = ts.get(t, managePath+"/reschedule?tz=Europe/Lisbon")
	mustContain(t, body, "Pick a new time", "Currently Mon 26 Oct, 10:00 – 10:30.")
	_, body, _ = ts.get(t, first(t, slotLinkRe, body))
	mustContain(t, body, "Move your Booking?", "Move to ")
	status, _, h = ts.post(t, managePath+"/reschedule", url.Values{"start": {first(t, startRe, body)}, "tz": {"Europe/Lisbon"}})
	if status != http.StatusSeeOther || !strings.Contains(h.Get("Location"), "changed=moved") {
		t.Fatalf("reschedule: status %d, location %q", status, h.Get("Location"))
	}
	_, body, _ = ts.get(t, h.Get("Location"))
	mustContain(t, body, "Your Booking was moved.")
	_, body, _ = ts.get(t, managePath+"/event.ics")
	mustContain(t, body, "SEQUENCE:1")

	// Cancel.
	status, _, h = ts.post(t, managePath+"/cancel", nil)
	if status != http.StatusSeeOther {
		t.Fatalf("cancel: status %d", status)
	}
	_, body, _ = ts.get(t, h.Get("Location"))
	mustContain(t, body, "Your Booking is cancelled.", "Cancelled by you")
	if strings.Contains(body, "Cancel this Booking") {
		t.Error("cancelled booking still offers cancel")
	}

	// The Owner sees it.
	_, body, _ = ts.get(t, "/admin/bookings")
	mustContain(t, body, "Alex", "Cancelled by Guest")
	_, body, _ = ts.get(t, "/admin/invites")
	mustContain(t, body, "Used up", "1 / 1")
}

func TestDayGrid(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	invite := ts.createInvite(t, defaultInvite)
	_, body, _ := ts.get(t, invite+"?tz=Europe/Lisbon")
	// The fortnight starts on Monday 26 October; weekends are off.
	mustContain(t, body, `aria-label="Sat 31 Oct, no times"`, `aria-current="date"`)
	days := dayLinkRe.FindAllStringSubmatch(body, -1)
	if len(days) != 10 {
		t.Fatalf("got %d open days, want 10 weekdays", len(days))
	}
	// Picking Wednesday shows Wednesday's Slots.
	_, body, _ = ts.get(t, html.UnescapeString(days[2][1]))
	mustContain(t, body, "Wed 28 October", "4 times")
	// The 14-day horizon ends on 8 November, inside this fortnight.
	if strings.Contains(body, `aria-label="Earlier"`) || strings.Contains(body, `aria-label="Later"`) {
		t.Error("unexpected fortnight navigation")
	}
}

func TestFortnightNavigation(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	// A 30-day horizon spans three fortnights from Monday 26 October.
	ts.Config.Handler.(*Server).svc.Availability.Horizon = 30 * 24 * time.Hour
	invite := ts.createInvite(t, defaultInvite)
	_, body, _ := ts.get(t, invite+"?tz=Europe/Lisbon")
	later := regexp.MustCompile(`href="([^"]+)" aria-label="Later"`).FindStringSubmatch(body)
	if later == nil || strings.Contains(body, `aria-label="Earlier"`) {
		t.Fatalf("first fortnight navigation wrong:\n%s", body)
	}
	_, body, _ = ts.get(t, html.UnescapeString(later[1]))
	mustContain(t, body, `aria-label="Earlier"`, "Mon 9 November")
	// A from= in the middle of a fortnight snaps to its start.
	_, body, _ = ts.get(t, invite+"?tz=Europe/Lisbon&from=2026-11-12")
	mustContain(t, body, "Mon 9 November")
}

func TestBusyBarOnlyWhenOptedIn(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	monday := time.Date(2026, 10, 26, 10, 0, 0, 0, lisbon)
	ts.cals.busy = []schedule.Interval{{Start: monday, End: monday.Add(time.Hour)}}

	hidden := ts.createInvite(t, defaultInvite)
	_, body, _ := ts.get(t, hidden+"?tz=Europe/Lisbon")
	if strings.Contains(body, `class="busybar"`) {
		t.Error("busy bar shown without opt-in")
	}

	form := cloneValues(defaultInvite)
	form.Set("show_busy", "1")
	shown := ts.createInvite(t, form)
	_, body, _ = ts.get(t, shown+"?tz=Europe/Lisbon")
	mustContain(t, body, `class="busybar"`, `Busy and free time, 10:00 to 12:00`, `<div class="busy"></div><div class="busy"></div><div class=""></div><div class=""></div>`)
}

func TestOwnerCancelsAndRevokes(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	manage := ts.book(t, "Maria")

	if status, _, _ := ts.post(t, "/admin/bookings/1/cancel", nil); status != http.StatusSeeOther {
		t.Fatalf("owner cancel: %d", status)
	}
	_, body, _ := ts.get(t, manage+"?tz=Europe/Lisbon")
	mustContain(t, body, "Cancelled by Nikolai", "Nikolai cancelled this Booking")

	form := cloneValues(defaultInvite)
	form.Set("max_uses", "3")
	invite := ts.createInvite(t, form)
	if status, _, _ := ts.post(t, "/admin/invites/2/revoke", nil); status != http.StatusSeeOther {
		t.Fatalf("revoke: %d", status)
	}
	if status, _, _ := ts.get(t, invite); status != http.StatusNotFound {
		t.Errorf("revoked invite: status %d", status)
	}
	_, body, _ = ts.get(t, "/admin/invites/2")
	mustContain(t, body, "Revoked")
	if strings.Contains(body, "Revoke Invite") {
		t.Error("revoked invite still offers revoke")
	}
}

func TestNewInvitePageAndErrors(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, body, _ := ts.get(t, "/admin/invites/new")
	mustContain(t, body, "Can change after sharing", "Fixed once shared", "Inside your Availability: Mon–Fri 10:00–12:00 (Lisbon)",
		"Book time with Nikolai", "open Slots", `value="7" selected`)

	status, body, _ := ts.post(t, "/admin/invites", url.Values{"label": {"oops"}, "max_uses": {"1"}, "expires_in_days": {"7"}, "location_kind": {"video"}})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", status)
	}
	mustContain(t, body, "Pick at least one duration.", `value="oops"`)
}

func TestInviteCreatedShowsLinkOnce(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	form := cloneValues(defaultInvite)
	form.Set("location_kind", "video")
	form.Set("location", "https://meet.example.com/abc")
	_, body, _ := ts.post(t, "/admin/invites", form)
	mustContain(t, body, "Copy the link now. It's shown only once.", "Video call · https://meet.example.com/abc")
	_, body, _ = ts.get(t, "/admin/invites/1")
	if inviteLinkRe.MatchString(body) {
		t.Error("invite link shown again")
	}
	// Guests see the kind before booking, not the link.
	invite := first(t, inviteLinkRe, func() string { _, b, _ := ts.post(t, "/admin/invites", form); return b }())
	_, body, _ = ts.get(t, invite+"?tz=Europe/Lisbon")
	mustContain(t, body, "Video call")
	if strings.Contains(body, "meet.example.com") {
		t.Error("video link shown before booking")
	}
}

func TestStaleCalendars(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	invite := ts.createInvite(t, defaultInvite)
	ts.cals.stale = true
	status, body, _ := ts.get(t, invite)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status %d", status)
	}
	mustContain(t, body, "Booking is paused for a moment")
	_, body, _ = ts.get(t, "/admin/invites")
	mustContain(t, body, "1 calendar source isn&#39;t current")
	_, body, _ = ts.get(t, "/admin/bookings")
	mustContain(t, body, "hasn&#39;t been read for 1 h 12 min")
	_, body, _ = ts.get(t, "/admin/sources")
	mustContain(t, body, "Not current", "HTTP 502 Bad Gateway", "https://calendar.example.com/…")
}

func TestAvailabilityPage(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, body, _ := ts.get(t, "/admin/availability")
	mustContain(t, body, "Monday", "10:00–12:00", "Saturday", "Unavailable", "Lisbon (UTC)", "14 days")
}

func TestAccentSetting(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, body, _ := ts.get(t, "/admin/settings")
	mustContain(t, body, "Accent color", `value="#2D3E8F" checked`, ":root{--accent:#2D3E8F}")

	status, body, _ := ts.post(t, "/admin/settings", url.Values{"custom": {"#F5D76E"}})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("light colour: status %d", status)
	}
	mustContain(t, body, "Too light")

	if status, _, _ := ts.post(t, "/admin/settings", url.Values{"accent": {"#1F5F5B"}}); status != http.StatusSeeOther {
		t.Fatalf("save: %d", status)
	}
	invite := ts.createInvite(t, defaultInvite)
	_, body, _ = ts.get(t, invite+"?tz=Europe/Lisbon")
	mustContain(t, body, ":root{--accent:#1F5F5B}")
	_, body, _ = ts.get(t, "/favicon.svg")
	mustContain(t, body, `stroke="#1F5F5B"`)
}

func TestStaticAssets(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, body, _ := ts.get(t, "/i/whatever")
	css := regexp.MustCompile(`href="(/static/hima\.css\?v=[0-9a-f]+)"`).FindStringSubmatch(body)
	if css == nil {
		t.Fatalf("no versioned stylesheet in:\n%s", body)
	}
	status, body, h := ts.get(t, html.UnescapeString(css[1]))
	if status != http.StatusOK || !strings.Contains(body, "--accent") || !strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Errorf("css: %d %q", status, h.Get("Cache-Control"))
	}
	if status, _, h := ts.get(t, "/static/fonts/zen-kaku-gothic-new-400-latin.woff2"); status != http.StatusOK || h.Get("Content-Type") != "font/woff2" {
		t.Errorf("font: %d %q", status, h.Get("Content-Type"))
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	req, _ := http.NewRequest("POST", ts.URL+"/admin/invites", strings.NewReader(defaultInvite.Encode()))
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
	if status, _, _ := ts.get(t, "/admin/invites"); status != http.StatusUnauthorized {
		t.Errorf("without header: status = %d, want 401", status)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/admin/invites", nil)
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

	status, _, h := ts.get(t, "/admin/invites")
	if status != http.StatusSeeOther || h.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous admin: status = %d, location = %q", status, h.Get("Location"))
	}
	if status, body, _ := ts.post(t, "/admin/login", url.Values{"password": {"wrong"}}); status != http.StatusUnauthorized || !strings.Contains(body, "Wrong password.") {
		t.Errorf("wrong password: status = %d", status)
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
	req, _ := http.NewRequest("GET", ts.URL+"/admin/invites", nil)
	req.AddCookie(cookies[0])
	resp, err = ts.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "New Invite") || !strings.Contains(string(body), "Log out") {
		t.Errorf("logged-in admin: status = %d", resp.StatusCode)
	}
}

func TestDeadInviteIsNeutral(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	status, body, _ := ts.get(t, "/i/whatever")
	if status != http.StatusNotFound {
		t.Errorf("status = %d", status)
	}
	mustContain(t, body, "Ask the person who sent it for a new one.")
	if strings.Contains(body, "Nikolai") {
		t.Error("dead invite page names the Owner")
	}
}

func TestOwnerFeed(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	_, body, _ := ts.get(t, "/admin/bookings")
	mustContain(t, body, "Create secret URL")

	manage := ts.book(t, "Alex")
	ts.post(t, manage+"/cancel", nil)
	ts.book(t, "Maria")

	_, body, _ = ts.post(t, "/admin/feed", nil)
	mustContain(t, body, "Shown only once.")
	feed := first(t, feedLinkRe, body)

	status, body, h := ts.get(t, feed)
	if status != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "text/calendar") {
		t.Fatalf("feed: %d %s", status, h.Get("Content-Type"))
	}
	mustContain(t, body, "X-WR-CALNAME:hima: bookings with Nikolai", "REFRESH-INTERVAL;VALUE=DURATION:PT15M",
		"SUMMARY:Alex ↔ Nikolai: hello", "STATUS:CANCELLED", "SUMMARY:Maria ↔ Nikolai: hello", "Booked via invite: for Maria")

	if status, _, _ := ts.get(t, strings.TrimSuffix(feed, ".ics")); status != http.StatusOK {
		t.Errorf("feed without suffix: %d", status)
	}
	if status, _, _ := ts.get(t, "/feed/AAAAAAAAAAAAAAAAAAAAAA.ics"); status != http.StatusNotFound {
		t.Errorf("wrong token: %d", status)
	}

	_, body, _ = ts.post(t, "/admin/feed", nil)
	if status, _, _ := ts.get(t, feed); status != http.StatusNotFound {
		t.Errorf("old feed after rotation: %d", status)
	}
	if status, _, _ := ts.get(t, first(t, feedLinkRe, body)); status != http.StatusOK {
		t.Errorf("new feed: %d", status)
	}
	_, body, _ = ts.get(t, "/admin/bookings")
	mustContain(t, body, "Active since", "New secret URL")
	if feedLinkRe.MatchString(body) {
		t.Error("feed URL shown again")
	}
}

func TestActivityLog(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	manage := ts.book(t, "Alex")
	ts.post(t, manage+"/cancel", nil)
	_, body, _ := ts.get(t, "/admin/activity")
	mustContain(t, body, "New booking: Alex", "Alex cancelled")
}

func TestOwnerEditsInviteAndBooking(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	manage := ts.book(t, "Alex") // 1 use: invite now used up

	_, body, _ := ts.get(t, "/admin/invites/1")
	mustContain(t, body, `action="/admin/invites/1"`, "Fixed, already shared", "1 / 1 used", "Alex")

	status, body, _ := ts.post(t, "/admin/invites/1", url.Values{"label": {"x"}, "max_uses": {"0"}, "expires_at": {"2026-11-05T18:00"}})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("bad edit: %d", status)
	}
	mustContain(t, body, "at least one use")

	status, _, h := ts.post(t, "/admin/invites/1", url.Values{"label": {"for Alex & Sam"}, "max_uses": {"2"}, "expires_at": {"2026-11-05T18:00"}})
	if status != http.StatusSeeOther {
		t.Fatalf("edit invite: %d", status)
	}
	_, body, _ = ts.get(t, h.Get("Location"))
	mustContain(t, body, "Saved.")
	_, body, _ = ts.get(t, "/admin/invites")
	mustContain(t, body, "for Alex &amp; Sam", "1 / 2", "in 11 days", ">Active<")

	_, body, _ = ts.get(t, "/admin/bookings")
	mustContain(t, body, `action="/admin/bookings/1"`)
	if status, _, _ := ts.post(t, "/admin/bookings/1", url.Values{"location": {"https://meet.jit.si/abc"}, "note": {"bring photos"}}); status != http.StatusSeeOther {
		t.Fatalf("edit booking: %d", status)
	}
	_, body, _ = ts.get(t, manage+"?tz=Europe/Lisbon")
	mustContain(t, body, `href="https://meet.jit.si/abc"`, "bring photos")
	_, body, _ = ts.get(t, manage+"/event.ics")
	mustContain(t, body, "SEQUENCE:1", "LOCATION:https://meet.jit.si/abc")
	_, body, _ = ts.get(t, "/admin/activity")
	mustContain(t, body, "You updated Alex&#39;s booking")
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{"/i/abc": "/i/…", "/b/abc/cancel": "/b/…/cancel", "/feed/x.ics": "/feed/…", "/admin/": "/admin/"} {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContrastOnWhite(t *testing.T) {
	for hex, ok := range map[string]bool{"#2D3E8F": true, "#16181A": true, "#FFFFFF": false, "#F5D76E": false, "#85570A": true} {
		if validAccent(hex) != ok {
			t.Errorf("validAccent(%s) = %v (ratio %.2f)", hex, !ok, contrastOnWhite(hex))
		}
	}
}
