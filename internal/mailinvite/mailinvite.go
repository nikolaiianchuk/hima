// Package mailinvite writes Bookings to the Owner's calendar by email, as
// calendar invitations (iTIP over email, RFC 6047): a REQUEST adds or updates
// the event, a CANCEL removes it. Any calendar that reads invitations from
// email (Gmail, Outlook, Apple Mail, …) picks them up; no calendar API or
// OAuth app is needed, only an SMTP account.
package mailinvite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// SMTP is the account invitations are sent through.
type SMTP struct {
	Host     string
	Port     int // 465: implicit TLS; otherwise STARTTLS when offered
	Username string
	Password string
}

// Target sends invitations to the Owner. It implements writeback.Target.
type Target struct {
	SMTP SMTP
	// From is the sender (and the invitation's organiser); To is the Owner.
	From, To  string
	OwnerName string
	// Loc formats times in the message text.
	Loc *time.Location
	Now func() time.Time
}

func (t *Target) Put(ctx context.Context, b store.Booking, ev ics.Event) error {
	verb := "Booking"
	if ev.Sequence > 0 {
		verb = "Updated"
	}
	return t.send(ctx, "REQUEST", verb, ev)
}

// Delete sends a cancellation. Its sequence must exceed the last update's.
func (t *Target) Delete(ctx context.Context, _ store.Booking, ev ics.Event) error {
	ev.Sequence++
	ev.Cancelled = true
	return t.send(ctx, "CANCEL", "Cancelled", ev)
}

// Calendar builds the iTIP object: the event with hima as organiser and the
// Owner as an attendee who has already accepted and owes no reply.
func (t *Target) Calendar(method string, ev ics.Event) *ical.Calendar {
	cal := ics.Build("", ev)
	cal.Props.SetText(ical.PropMethod, method)
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		org := ical.NewProp(ical.PropOrganizer)
		org.Value = "mailto:" + t.From
		org.Params.Set(ical.ParamCommonName, "hima")
		c.Props.Set(org)
		att := ical.NewProp(ical.PropAttendee)
		att.Value = "mailto:" + t.To
		att.Params.Set(ical.ParamCommonName, t.OwnerName)
		att.Params.Set(ical.ParamParticipationStatus, "ACCEPTED")
		att.Params.Set(ical.ParamRSVP, "FALSE")
		att.Params.Set(ical.ParamRole, "REQ-PARTICIPANT")
		c.Props.Set(att)
	}
	return cal
}

// Message builds the email: a text part and the calendar part as
// alternatives, plus the same calendar attached as invite.ics.
func (t *Target) Message(method, verb string, ev ics.Event) ([]byte, error) {
	var calBuf bytes.Buffer
	if err := ical.NewEncoder(&calBuf).Encode(t.Calendar(method, ev)); err != nil {
		return nil, err
	}
	loc := t.Loc
	if loc == nil {
		loc = time.UTC
	}
	when := ev.Start.In(loc).Format("Mon 2 Jan 15:04") + "–" + ev.End.In(loc).Format("15:04")
	text := verb + ": " + ev.Summary + "\r\n" + when + " (" + loc.String() + ")\r\n"
	if ev.Location != "" {
		text += "Where: " + ev.Location + "\r\n"
	}
	if ev.Notes != "" {
		text += "\r\n" + strings.ReplaceAll(ev.Notes, "\n", "\r\n") + "\r\n"
	}
	text += "\r\nSent by hima. Your calendar should add, update or remove this event by itself.\r\n"

	mixed, alt := boundary(), boundary()
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	var m bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&m, "%s: %s\r\n", k, v) }
	header("From", "hima <"+t.From+">")
	header("To", t.To)
	header("Subject", mime.QEncoding.Encode("utf-8", verb+": "+ev.Summary+", "+when))
	header("Date", now().Format(time.RFC1123Z))
	header("Message-ID", "<"+boundary()+"@"+domainOf(t.From)+">")
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/mixed; boundary="`+mixed+`"`)
	m.WriteString("\r\n--" + mixed + "\r\n")
	m.WriteString(`Content-Type: multipart/alternative; boundary="` + alt + `"` + "\r\n\r\n")
	part(&m, alt, "text/plain; charset=UTF-8", "", []byte(text))
	part(&m, alt, "text/calendar; charset=UTF-8; method="+method, "", calBuf.Bytes())
	m.WriteString("--" + alt + "--\r\n")
	part(&m, mixed, "application/ics; name=invite.ics", `attachment; filename="invite.ics"`, calBuf.Bytes())
	m.WriteString("--" + mixed + "--\r\n")
	return m.Bytes(), nil
}

func part(m *bytes.Buffer, boundary, contentType, disposition string, body []byte) {
	m.WriteString("--" + boundary + "\r\n")
	m.WriteString("Content-Type: " + contentType + "\r\n")
	if disposition != "" {
		m.WriteString("Content-Disposition: " + disposition + "\r\n")
	}
	m.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString(body)
	for len(enc) > 76 {
		m.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	m.WriteString(enc + "\r\n")
}

func boundary() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "hima-" + hex.EncodeToString(b)
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return addr[i+1:]
	}
	return "hima.invalid"
}

func (t *Target) send(ctx context.Context, method, verb string, ev ics.Event) error {
	msg, err := t.Message(method, verb, ev)
	if err != nil {
		return err
	}
	return t.SMTP.Send(ctx, t.From, t.To, msg)
}

// Send delivers msg. Port 465 uses TLS from the start; other ports upgrade
// with STARTTLS when the server offers it. Credentials are only sent over
// TLS (or to localhost), which net/smtp enforces.
func (s SMTP) Send(ctx context.Context, from, to string, msg []byte) error {
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	var conn net.Conn
	var err error
	if s.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: s.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if s.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
				return err
			}
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
