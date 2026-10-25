package notify

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestTelegram(t *testing.T) {
	st := newStore(t)
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/sendDocument") {
			r.ParseMultipartForm(1 << 20)
			f, h, _ := r.FormFile("document")
			data, _ := io.ReadAll(f)
			got = append(got, r.URL.Path+" "+r.FormValue("chat_id")+" "+h.Filename+" "+string(data)+" "+r.FormValue("caption"))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, fmt.Sprint(r.URL.Path, " ", body["chat_id"], " ", body["text"]))
	}))
	defer srv.Close()
	d := &Dispatcher{Store: st, Log: discard, Telegram: &TelegramConfig{BotToken: "123:abc", ChatID: "42", AttachICS: true, APIBase: srv.URL}}
	d.Notify(context.Background(), Event{Kind: Booked, At: time.Now(), Title: "New booking: Zoë", Message: "Mon 26 Oct",
		Attachment: &Attachment{Filename: "booking.ics", Data: []byte("ICS")}})
	d.Notify(context.Background(), Event{Kind: CancelledByGuest, At: time.Now(), Title: "Zoë cancelled", Message: "Mon 26 Oct"})
	d.Wait(context.Background())
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(got, "\n")
	for _, want := range []string{"/bot123:abc/sendDocument 42 booking.ics ICS 🗓 <b>New booking: Zoë</b>\nMon 26 Oct", "/bot123:abc/sendMessage 42 ❌ <b>Zoë cancelled</b>\nMon 26 Oct"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
}

func TestTelegramErrorsHideToken(t *testing.T) {
	d := &Dispatcher{Log: discard, Telegram: &TelegramConfig{BotToken: "123:secret", ChatID: "1", APIBase: "http://127.0.0.1:1"}}
	err := d.sendTelegram(context.Background(), Event{Title: "x"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v", err)
	}
}

func TestTelegramHTML(t *testing.T) {
	booked := telegramHTML(Event{Kind: Booked, Title: "New booking: Alex <script>", Booking: &BookingInfo{
		When: "Mon 26 Oct, 10:00–10:30", Location: "https://meet.example.com/a?b=1&c=2", Note: "bring <photos> & maps", InviteLabel: "for Alex",
	}})
	want := "🗓 <b>New booking: Alex &lt;script&gt;</b>\n" +
		"🕐 Mon 26 Oct, 10:00–10:30\n" +
		`📍 <a href="https://meet.example.com/a?b=1&amp;c=2">https://meet.example.com/a?b=1&amp;c=2</a>` + "\n" +
		"💬 <i>bring &lt;photos&gt; &amp; maps</i>\n" +
		"🏷 Invite “for Alex”"
	if booked != want {
		t.Errorf("booked:\n%s\nwant:\n%s", booked, want)
	}
	moved := telegramHTML(Event{Kind: Rescheduled, Title: "Alex rescheduled", Booking: &BookingInfo{When: "Tue 27 Oct, 11:00–11:30", PreviousWhen: "Mon 26 Oct, 10:00–10:30", Location: "Café"}})
	if !strings.Contains(moved, "🕐 <s>Mon 26 Oct, 10:00–10:30</s>\n➡️ <b>Tue 27 Oct, 11:00–11:30</b>\n📍 Café") {
		t.Errorf("moved:\n%s", moved)
	}
	cancelled := telegramHTML(Event{Kind: CancelledByGuest, Title: "Alex cancelled", Booking: &BookingInfo{When: "Mon 26 Oct, 10:00–10:30"}})
	if !strings.Contains(cancelled, "❌ <b>Alex cancelled</b>\n🕐 <s>Mon 26 Oct, 10:00–10:30</s>") {
		t.Errorf("cancelled:\n%s", cancelled)
	}
}

func TestTelegramButtonOnlyForHTTPS(t *testing.T) {
	d := &Dispatcher{ClickURL: "http://localhost:8080/admin/"}
	if d.telegramButton() != nil {
		t.Error("button for http URL")
	}
	d.ClickURL = "https://hima.example.com/admin/"
	if d.telegramButton() == nil {
		t.Error("no button for https URL")
	}
}
