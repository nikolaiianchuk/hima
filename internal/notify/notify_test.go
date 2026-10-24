package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nikolaiianchuk/hima/internal/store"
)

func newStore(t *testing.T) *store.Store {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hima.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type recorder struct {
	mu       sync.Mutex
	failures atomic.Int32 // respond 500 this many times first
	reqs     []*http.Request
	bodies   []string
}

func (rec *recorder) server(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if rec.failures.Add(-1) >= 0 {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, r)
		rec.bodies = append(rec.bodies, string(body))
		rec.mu.Unlock()
	}))
	t.Cleanup(s.Close)
	return s
}

func TestNtfyAndWebhookWithRetry(t *testing.T) {
	st := newStore(t)
	var ntfy, hook recorder
	ntfy.failures.Store(2) // succeeds on the third attempt
	ntfySrv, hookSrv := ntfy.server(t), hook.server(t)

	d := &Dispatcher{
		Store: st, Ntfy: &NtfyConfig{URL: ntfySrv.URL + "/topic", Token: "tk"}, Webhook: hookSrv.URL,
		Log: discard, Backoff: []time.Duration{time.Millisecond, time.Millisecond}, ClickURL: "https://hima.example.com/admin/",
	}
	start := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	d.Notify(context.Background(), Event{
		Kind: Booked, At: start, Title: "New booking: Zoë", Message: "Mon 26 Oct, 10:00–10:30",
		Booking: &BookingInfo{GuestName: "Zoë", Start: start, End: start.Add(30 * time.Minute)},
	})
	d.Wait(context.Background())

	if len(ntfy.reqs) != 1 {
		t.Fatalf("ntfy received %d successful requests", len(ntfy.reqs))
	}
	r := ntfy.reqs[0]
	title, _ := new(mime.WordDecoder).DecodeHeader(r.Header.Get("Title"))
	if title != "New booking: Zoë" || r.Header.Get("Authorization") != "Bearer tk" || r.Header.Get("Tags") != "calendar" ||
		r.Header.Get("Click") != "https://hima.example.com/admin/" || ntfy.bodies[0] != "Mon 26 Oct, 10:00–10:30" {
		t.Errorf("ntfy request: title=%q headers=%v body=%q", title, r.Header, ntfy.bodies[0])
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(hook.bodies[0]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["kind"] != "booked" || payload["booking"].(map[string]any)["guest_name"] != "Zoë" {
		t.Errorf("webhook payload %v", payload)
	}

	acts, _ := st.RecentActivity(context.Background(), 10)
	if len(acts) != 1 || acts[0].Title != "New booking: Zoë" {
		t.Errorf("activity %v", acts)
	}
}

func TestSilentEventsAreOnlyLogged(t *testing.T) {
	st := newStore(t)
	var hook recorder
	srv := hook.server(t)
	d := &Dispatcher{Store: st, Webhook: srv.URL, Log: discard}
	d.Notify(context.Background(), Event{Kind: CancelledByOwner, At: time.Now(), Title: "You cancelled", Silent: true})
	d.Wait(context.Background())
	if len(hook.reqs) != 0 {
		t.Error("silent event was pushed")
	}
	if acts, _ := st.RecentActivity(context.Background(), 10); len(acts) != 1 {
		t.Errorf("activity %v", acts)
	}
}

func TestGivesUpAfterRetries(t *testing.T) {
	st := newStore(t)
	var hook recorder
	hook.failures.Store(100)
	srv := hook.server(t)
	d := &Dispatcher{Store: st, Webhook: srv.URL, Log: discard, Backoff: []time.Duration{time.Millisecond}}
	d.Notify(context.Background(), Event{Kind: Booked, At: time.Now(), Title: "x"})
	d.Wait(context.Background())
	if got := 100 - hook.failures.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

func TestNtfyAttachment(t *testing.T) {
	st := newStore(t)
	var ntfy recorder
	srv := ntfy.server(t)
	d := &Dispatcher{Store: st, Ntfy: &NtfyConfig{URL: srv.URL, AttachICS: true}, Log: discard}
	d.Notify(context.Background(), Event{
		Kind: Booked, At: time.Now(), Title: "New booking: Zoë", Message: "Mon 26 Oct · “café”",
		Attachment: &Attachment{Filename: "booking.ics", Data: []byte("BEGIN:VCALENDAR")},
	})
	d.Wait(context.Background())
	if len(ntfy.reqs) != 1 {
		t.Fatalf("requests: %d", len(ntfy.reqs))
	}
	r := ntfy.reqs[0]
	msg, _ := new(mime.WordDecoder).DecodeHeader(r.Header.Get("Message"))
	if ntfy.bodies[0] != "BEGIN:VCALENDAR" || r.Header.Get("Filename") != "booking.ics" || msg != "Mon 26 Oct · “café”" {
		t.Errorf("body=%q filename=%q message=%q", ntfy.bodies[0], r.Header.Get("Filename"), msg)
	}
}

func TestNtfyAttachmentIgnoredUnlessEnabled(t *testing.T) {
	st := newStore(t)
	var ntfy recorder
	srv := ntfy.server(t)
	d := &Dispatcher{Store: st, Ntfy: &NtfyConfig{URL: srv.URL}, Log: discard}
	d.Notify(context.Background(), Event{Kind: Booked, At: time.Now(), Title: "t", Message: "plain",
		Attachment: &Attachment{Filename: "booking.ics", Data: []byte("ICS")}})
	d.Wait(context.Background())
	if ntfy.bodies[0] != "plain" || ntfy.reqs[0].Header.Get("Filename") != "" {
		t.Errorf("body=%q", ntfy.bodies[0])
	}
}

func TestNtfyFallsBackWhenAttachmentRefused(t *testing.T) {
	st := newStore(t)
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		if r.Header.Get("Filename") != "" {
			http.Error(w, `{"code":40014,"error":"attachments not allowed"}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	d := &Dispatcher{Store: st, Ntfy: &NtfyConfig{URL: srv.URL, AttachICS: true}, Log: discard, Backoff: []time.Duration{time.Millisecond}}
	d.Notify(context.Background(), Event{Kind: Booked, At: time.Now(), Title: "t", Message: "plain",
		Attachment: &Attachment{Filename: "booking.ics", Data: []byte("ICS")}})
	d.Wait(context.Background())
	if len(bodies) != 2 || bodies[0] != "ICS" || bodies[1] != "plain" {
		t.Errorf("requests = %q, want attachment then plain fallback", bodies)
	}
}

func TestStatusErrorIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"attachments not allowed"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest("POST", srv.URL, nil)
	err := do(http.DefaultClient, req)
	if err == nil || !strings.Contains(err.Error(), "attachments not allowed") {
		t.Errorf("err = %v", err)
	}
}
