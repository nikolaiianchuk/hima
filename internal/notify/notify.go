// Package notify tells the Owner what happened: it records every Event in the
// activity log and delivers it to ntfy and/or a webhook in the background.
// Delivery failures are retried and logged, never surfaced to the caller, so
// a notification can never undo a Booking (see CONTEXT.md).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nikolaiianchuk/hima/internal/store"
)

type Kind string

const (
	Booked            Kind = "booked"
	Rescheduled       Kind = "rescheduled"
	CancelledByGuest  Kind = "cancelled_by_guest"
	CancelledByOwner  Kind = "cancelled_by_owner"
	CalendarFailing   Kind = "calendar_failing"
	CalendarRecovered Kind = "calendar_recovered"
)

// Event is something the Owner should know about.
type Event struct {
	Kind    Kind
	At      time.Time
	Title   string
	Message string
	// Booking is set for Booking events.
	Booking *BookingInfo
	// Silent events are logged in the activity log but not pushed, e.g. the
	// Owner's own actions.
	Silent bool
	// Attachment, if set, may be attached by channels that support files.
	Attachment *Attachment
}

// Attachment is a file offered alongside an Event, e.g. a Booking's .ics.
type Attachment struct {
	Filename string
	Data     []byte
}

type BookingInfo struct {
	GuestName   string    `json:"guest_name"`
	Note        string    `json:"note,omitempty"`
	Location    string    `json:"location,omitempty"`
	InviteLabel string    `json:"invite_label,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
}

// Notifier receives Events. A nil Notifier is valid and does nothing.
type Notifier interface {
	Notify(ctx context.Context, ev Event)
}

type NtfyConfig struct {
	// URL is the full topic URL, e.g. https://ntfy.sh/my-secret-topic.
	URL string
	// Token is an optional access token for protected topics.
	Token string
	// AttachICS sends a Booking's .ics as a file with new-Booking and
	// reschedule notifications. Needs a server with attachments enabled; otherwise the
	// plain notification is sent instead.
	AttachICS bool
}

type Dispatcher struct {
	Store   *store.Store
	Ntfy    *NtfyConfig
	Webhook string
	Client  *http.Client
	Log     *slog.Logger
	// Backoff is the wait before each retry; its length is the retry count.
	Backoff []time.Duration
	// ClickURL, if set, is where tapping an ntfy notification leads (the admin).
	ClickURL string

	wg sync.WaitGroup
}

func (d *Dispatcher) Notify(ctx context.Context, ev Event) {
	if err := d.Store.AddActivity(ctx, store.Activity{At: ev.At, Kind: string(ev.Kind), Title: ev.Title, Message: ev.Message}); err != nil {
		d.Log.Error("record activity", "err", err)
	}
	if ev.Silent {
		return
	}
	if d.Ntfy != nil && d.Ntfy.URL != "" {
		d.deliver("ntfy", func(ctx context.Context) error { return d.sendNtfy(ctx, ev) })
	}
	if d.Webhook != "" {
		d.deliver("webhook", func(ctx context.Context) error { return d.sendWebhook(ctx, ev) })
	}
}

// Wait blocks until in-flight deliveries finish or ctx is done.
func (d *Dispatcher) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (d *Dispatcher) deliver(channel string, send func(context.Context) error) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		var err error
		for attempt := 0; attempt <= len(d.Backoff); attempt++ {
			if attempt > 0 {
				time.Sleep(d.Backoff[attempt-1])
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err = send(ctx)
			cancel()
			if err == nil {
				return
			}
		}
		d.Log.Warn("notification failed", "channel", channel, "err", err)
	}()
}

func (d *Dispatcher) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

var ntfyTags = map[Kind]string{
	Booked:            "calendar",
	Rescheduled:       "arrows_counterclockwise",
	CancelledByGuest:  "x",
	CalendarFailing:   "warning",
	CalendarRecovered: "white_check_mark",
}

// sendNtfy pushes ev, with its attachment if enabled. If the server refuses
// the attachment (a 4xx, e.g. attachments disabled or too large), it falls
// back to the plain notification at once rather than retrying.
func (d *Dispatcher) sendNtfy(ctx context.Context, ev Event) error {
	if d.Ntfy.AttachICS && ev.Attachment != nil {
		err := d.postNtfy(ctx, ev, ev.Attachment)
		var status *statusError
		if !errors.As(err, &status) || status.code >= 500 {
			return err
		}
		d.Log.Warn("ntfy refused the attachment, sending without it", "err", err)
	}
	return d.postNtfy(ctx, ev, nil)
}

func (d *Dispatcher) postNtfy(ctx context.Context, ev Event, att *Attachment) error {
	body := []byte(ev.Message)
	if att != nil {
		body = att.Data
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Ntfy.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if att != nil {
		// With a file as the body, the text travels in the Message header.
		req.Header.Set("Filename", att.Filename)
		req.Header.Set("Message", mimeEncode(ev.Message))
	}
	// ntfy reads RFC 2047 encoded headers, so non-ASCII titles survive.
	req.Header.Set("Title", mimeEncode(ev.Title))
	if tag := ntfyTags[ev.Kind]; tag != "" {
		req.Header.Set("Tags", tag)
	}
	if ev.Kind == CalendarFailing {
		req.Header.Set("Priority", "high")
	}
	if d.ClickURL != "" {
		req.Header.Set("Click", d.ClickURL)
	}
	if d.Ntfy.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Ntfy.Token)
	}
	return do(d.client(), req)
}

type webhookPayload struct {
	Kind    Kind         `json:"kind"`
	At      time.Time    `json:"at"`
	Title   string       `json:"title"`
	Message string       `json:"message"`
	Booking *BookingInfo `json:"booking,omitempty"`
}

func (d *Dispatcher) sendWebhook(ctx context.Context, ev Event) error {
	body, err := json.Marshal(webhookPayload{Kind: ev.Kind, At: ev.At, Title: ev.Title, Message: ev.Message, Booking: ev.Booking})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "hima")
	return do(d.client(), req)
}

func do(c *http.Client, req *http.Request) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &statusError{code: resp.StatusCode, status: resp.Status, body: strings.TrimSpace(string(body))}
	}
	return nil
}

type statusError struct {
	code   int
	status string
	body   string // start of the response, e.g. ntfy's JSON error
}

func (e *statusError) Error() string {
	if e.body == "" {
		return "HTTP " + e.status
	}
	return "HTTP " + e.status + ": " + e.body
}
