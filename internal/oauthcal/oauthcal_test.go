package oauthcal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// fakeAPI plays the token endpoint and both providers' REST APIs.
type fakeAPI struct {
	mu       sync.Mutex
	calls    []string
	events   map[string]bool // Google event IDs that exist
	refreshs int
}

func (f *fakeAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/token" {
			r.ParseForm()
			access := "at-code"
			if r.Form.Get("grant_type") == "refresh_token" {
				f.refreshs++
				access = "at-refreshed"
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"access_token":"`+access+`","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer at-") {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/oauth2/v2/userinfo":
			io.WriteString(w, `{"email":"me@gmail.com"}`)
		case r.URL.Path == "/calendar/v3/freeBusy":
			io.WriteString(w, `{"calendars":{"primary":{"busy":[{"start":"2026-10-26T09:00:00Z","end":"2026-10-26T10:00:00Z"}]}}}`)
		case strings.HasPrefix(r.URL.Path, "/calendar/v3/calendars/primary/events"):
			id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/calendar/v3/calendars/primary/events"), "/")
			switch r.Method {
			case http.MethodPut:
				if !f.events[id] {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
			case http.MethodPost:
				var body struct{ ID string }
				json.NewDecoder(r.Body).Decode(&body)
				f.events[body.ID] = true
			case http.MethodDelete:
				if !f.events[id] {
					http.Error(w, "gone", http.StatusGone)
					return
				}
				delete(f.events, id)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			io.WriteString(w, `{}`)
		case r.URL.Path == "/me":
			io.WriteString(w, `{"mail":"me@outlook.com"}`)
		case r.URL.Path == "/me/calendar/getSchedule":
			io.WriteString(w, `{"value":[{"scheduleItems":[
				{"status":"busy","start":{"dateTime":"2026-10-26T09:00:00.0000000","timeZone":"UTC"},"end":{"dateTime":"2026-10-26T10:00:00.0000000","timeZone":"UTC"}},
				{"status":"free","start":{"dateTime":"2026-10-26T11:00:00.0000000","timeZone":"UTC"},"end":{"dateTime":"2026-10-26T12:00:00.0000000","timeZone":"UTC"}},
				{"status":"oof","start":{"dateTime":"2026-10-27T00:00:00.0000000","timeZone":"UTC"},"end":{"dateTime":"2026-10-28T00:00:00.0000000","timeZone":"UTC"}}]}]}`)
		case r.URL.Path == "/me/events" && r.Method == http.MethodPost:
			io.WriteString(w, `{"id":"AAMk1"}`)
		case r.URL.Path == "/me/events/AAMk1":
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func setup(t *testing.T, newProvider func(*store.Store) *Provider) (*Provider, *fakeAPI, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hima.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := &fakeAPI{events: map[string]bool{}}
	srv := httptest.NewServer(api.handler(t))
	t.Cleanup(srv.Close)
	p := newProvider(st)
	p.OAuth.Endpoint = oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
	p.APIBase = srv.URL
	return p, api, st
}

var now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }

func TestNotConnected(t *testing.T) {
	p, _, _ := setup(t, func(st *store.Store) *Provider { return NewGoogle("id", "secret", "https://hima.test/cb", st, false) })
	_, err := (&GoogleBusy{P: p, Horizon: time.Hour, Now: now}).FetchBusy(context.Background())
	if !errors.Is(err, calsource.ErrNotConnected) {
		t.Errorf("err = %v, want ErrNotConnected", err)
	}
}

func TestGoogleConnectReadWrite(t *testing.T) {
	p, api, _ := setup(t, func(st *store.Store) *Provider { return NewGoogle("id", "secret", "https://hima.test/cb", st, true) })
	ctx := context.Background()
	if u := p.AuthURL("st8"); !strings.Contains(u, "access_type=offline") || !strings.Contains(u, "calendar.events") || !strings.Contains(u, "state=st8") {
		t.Errorf("auth URL %s", u)
	}
	if err := p.Exchange(ctx, "code"); err != nil {
		t.Fatal(err)
	}
	if acc, _ := p.Account(ctx); acc != "me@gmail.com" {
		t.Errorf("account %q", acc)
	}

	busy, err := (&GoogleBusy{P: p, Horizon: 30 * 24 * time.Hour, Now: now}).FetchBusy(ctx)
	if err != nil || len(busy) != 1 || busy[0].Start.Hour() != 9 {
		t.Fatalf("busy = %v, %v", busy, err)
	}

	w := &GoogleWriter{P: p}
	b := store.Booking{ID: 3}
	ev := ics.Event{UID: "booking-3@hima.test", Summary: "Alex ↔ Nikolai", Start: now(), End: now().Add(time.Hour)}
	if err := w.Put(ctx, b, ev); err != nil { // PUT 404 → POST
		t.Fatal(err)
	}
	if err := w.Put(ctx, b, ev); err != nil { // PUT updates
		t.Fatal(err)
	}
	if err := w.Delete(ctx, b, ev); err != nil {
		t.Fatal(err)
	}
	if err := w.Delete(ctx, b, ev); err != nil { // already gone
		t.Errorf("second delete: %v", err)
	}
	id := googleEventID(ev.UID)
	want := []string{"PUT /calendar/v3/calendars/primary/events/" + id, "POST /calendar/v3/calendars/primary/events",
		"PUT /calendar/v3/calendars/primary/events/" + id, "DELETE /calendar/v3/calendars/primary/events/" + id}
	got := strings.Join(api.calls, "\n")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing call %q in\n%s", w, got)
		}
	}
	for _, c := range id[4:] {
		if !strings.ContainsRune("0123456789abcdefghijklmnopqrstuv", c) {
			t.Fatalf("event id %q has %q outside Google's alphabet", id, c)
		}
	}

	if err := p.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := (&GoogleBusy{P: p, Now: now}).FetchBusy(ctx); !errors.Is(err, calsource.ErrNotConnected) {
		t.Errorf("after disconnect: %v", err)
	}
}

func TestRefreshedTokenIsStored(t *testing.T) {
	p, api, st := setup(t, func(st *store.Store) *Provider { return NewGoogle("id", "secret", "https://hima.test/cb", st, false) })
	ctx := context.Background()
	expired, _ := json.Marshal(&oauth2.Token{AccessToken: "at-old", RefreshToken: "rt", Expiry: time.Now().Add(-time.Hour)})
	st.SetSetting(ctx, p.tokenKey(), string(expired))
	if _, err := (&GoogleBusy{P: p, Horizon: time.Hour, Now: now}).FetchBusy(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := st.Setting(ctx, p.tokenKey())
	if api.refreshs != 1 || !strings.Contains(raw, "at-refreshed") {
		t.Errorf("refreshes = %d, stored = %s", api.refreshs, raw)
	}
}

func TestMicrosoftReadWrite(t *testing.T) {
	p, api, st := setup(t, func(st *store.Store) *Provider {
		return NewMicrosoft("id", "secret", "", "https://hima.test/cb", st, true)
	})
	ctx := context.Background()
	if err := p.Exchange(ctx, "code"); err != nil {
		t.Fatal(err)
	}
	busy, err := (&MicrosoftBusy{P: p, Horizon: 30 * 24 * time.Hour, Now: now}).FetchBusy(ctx)
	if err != nil || len(busy) != 2 {
		t.Fatalf("busy = %v, %v (want busy and oof, not free)", busy, err)
	}

	// The Booking must exist for the external ID's foreign key.
	invID, _ := st.CreateInvite(ctx, store.Invite{Durations: []time.Duration{time.Hour}, MaxUses: 1, ExpiresAt: now().Add(time.Hour), CreatedAt: now()}, "h")
	bID, err := st.CreateBooking(ctx, store.Booking{InviteID: invID, GuestName: "Alex", Start: now().Add(time.Hour), End: now().Add(2 * time.Hour), CreatedAt: now()}, "m", 0, store.DayCap{}, now())
	if err != nil {
		t.Fatal(err)
	}
	b := store.Booking{ID: bID}
	ev := ics.Event{UID: "booking-1@hima.test", Start: now(), End: now().Add(time.Hour)}
	w := &MicrosoftWriter{P: p}
	if err := w.Put(ctx, b, ev); err != nil {
		t.Fatal(err)
	}
	if id, _ := st.ExternalID(ctx, bID, "microsoft"); id != "AAMk1" {
		t.Fatalf("external id %q", id)
	}
	if err := w.Put(ctx, b, ev); err != nil {
		t.Fatal(err)
	}
	if err := w.Delete(ctx, b, ev); err != nil {
		t.Fatal(err)
	}
	if id, _ := st.ExternalID(ctx, bID, "microsoft"); id != "" {
		t.Errorf("external id kept after delete: %q", id)
	}
	got := strings.Join(api.calls, " | ")
	for _, want := range []string{"POST /me/events", "PATCH /me/events/AAMk1", "DELETE /me/events/AAMk1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func TestWindowChunks(t *testing.T) {
	chunks := window(now(), 90*24*time.Hour)
	if len(chunks) != 2 || !chunks[0][1].Equal(chunks[1][0]) {
		t.Errorf("chunks = %v", chunks)
	}
}
