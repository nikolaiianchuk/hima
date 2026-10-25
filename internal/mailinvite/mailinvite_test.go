package mailinvite

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// fakeSMTP accepts mail on localhost and keeps what it received.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	auth []string
	msgs []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(s string) { io.WriteString(conn, s+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		switch up := strings.ToUpper(cmd); {
		case strings.HasPrefix(up, "EHLO"):
			say("250-fake")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			f.mu.Lock()
			f.auth = append(f.auth, strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(up, "MAIL"), strings.HasPrefix(up, "RCPT"):
			say("250 ok")
		case up == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.msgs = append(f.msgs, b.String())
			f.mu.Unlock()
			say("250 queued")
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// calendarPart returns the decoded text/calendar alternative and its method.
func calendarPart(t *testing.T, raw string) (subject, method, body string) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ = new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	var walk func(io.Reader, string)
	walk = func(r io.Reader, contentType string) {
		mt, params, _ := mime.ParseMediaType(contentType)
		if !strings.HasPrefix(mt, "multipart/") {
			return
		}
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				return
			}
			ct := p.Header.Get("Content-Type")
			if pt, pp, _ := mime.ParseMediaType(ct); pt == "text/calendar" {
				data, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, p))
				method, body = pp["method"], string(data)
			}
			walk(p, ct)
		}
	}
	walk(msg.Body, msg.Header.Get("Content-Type"))
	return subject, method, body
}

func TestInvitationsOverSMTP(t *testing.T) {
	srv := newFakeSMTP(t)
	port, _ := strconv.Atoi(srv.ln.Addr().(*net.TCPAddr).AddrPort().String()[len("127.0.0.1:"):])
	target := &Target{
		SMTP:      SMTP{Host: "127.0.0.1", Port: port, Username: "hima@example.com", Password: "app-pw"},
		From:      "hima@example.com",
		To:        "nikolai@example.com",
		OwnerName: "Nikolai",
		Loc:       time.UTC,
	}
	start := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	ev := ics.Event{UID: "booking-1@hima.example.com", Summary: "Alex ↔ Nikolai: trip", Start: start, End: start.Add(30 * time.Minute), Location: "Café", Stamp: start}
	ctx := context.Background()
	if err := target.Put(ctx, store.Booking{ID: 1}, ev); err != nil {
		t.Fatal(err)
	}
	ev.Sequence = 1
	if err := target.Put(ctx, store.Booking{ID: 1}, ev); err != nil {
		t.Fatal(err)
	}
	if err := target.Delete(ctx, store.Booking{ID: 1}, ev); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.msgs) != 3 {
		t.Fatalf("got %d messages", len(srv.msgs))
	}
	creds, _ := base64.StdEncoding.DecodeString(srv.auth[0])
	if string(creds) != "\x00hima@example.com\x00app-pw" {
		t.Errorf("auth = %q", creds)
	}

	subject, method, cal := calendarPart(t, srv.msgs[0])
	if subject != "Booking: Alex ↔ Nikolai: trip, Mon 26 Oct 10:00–10:30" || method != "REQUEST" {
		t.Errorf("first: subject %q, method %q", subject, method)
	}
	unfolded := strings.ReplaceAll(cal, "\r\n ", "")
	for _, want := range []string{"METHOD:REQUEST", "UID:booking-1@hima.example.com", "SEQUENCE:0",
		"ORGANIZER;CN=hima:mailto:hima@example.com", "PARTSTAT=ACCEPTED", "RSVP=FALSE", "mailto:nikolai@example.com", "LOCATION:Café"} {
		if !strings.Contains(unfolded, want) {
			t.Errorf("request missing %q:\n%s", want, cal)
		}
	}

	subject, _, cal = calendarPart(t, srv.msgs[1])
	if !strings.HasPrefix(subject, "Updated: ") || !strings.Contains(cal, "SEQUENCE:1") {
		t.Errorf("update: %q\n%s", subject, cal)
	}

	subject, method, cal = calendarPart(t, srv.msgs[2])
	if !strings.HasPrefix(subject, "Cancelled: ") || method != "CANCEL" {
		t.Errorf("cancel: subject %q, method %q", subject, method)
	}
	for _, want := range []string{"METHOD:CANCEL", "STATUS:CANCELLED", "SEQUENCE:2", "UID:booking-1@hima.example.com"} {
		if !strings.Contains(cal, want) {
			t.Errorf("cancel missing %q:\n%s", want, cal)
		}
	}
	if !strings.Contains(srv.msgs[0], `filename="invite.ics"`) {
		t.Error("no invite.ics attachment")
	}
}
