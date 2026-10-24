package oauthcal

import (
	"context"
	"crypto/sha1"
	"encoding/base32"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// NewGoogle configures Google Calendar. write adds the scope to create
// events; reading busy time needs only the free/busy scope.
func NewGoogle(clientID, clientSecret, redirectURL string, st *store.Store, write bool) *Provider {
	scopes := []string{"https://www.googleapis.com/auth/calendar.freebusy", "https://www.googleapis.com/auth/userinfo.email"}
	if write {
		scopes = append(scopes, "https://www.googleapis.com/auth/calendar.events")
	}
	p := &Provider{
		ID:    "google",
		Title: "Google Calendar",
		OAuth: &oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
			Endpoint: endpoints.Google, Scopes: scopes,
		},
		Store:   st,
		APIBase: "https://www.googleapis.com",
		// Offline access and a forced consent screen guarantee a refresh token.
		AuthOptions: []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")},
	}
	p.fetchAccount = func(ctx context.Context, c *http.Client) (string, error) {
		resp, err := c.Get(p.APIBase + "/oauth2/v2/userinfo")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var info struct{ Email string }
		if err := decodeOK(resp, &info); err != nil {
			return "", err
		}
		return info.Email, nil
	}
	return p
}

// window is the range busy time is read for: a day of slack either side of
// the booking horizon, split into chunks the APIs accept.
func window(now time.Time, horizon time.Duration) [][2]time.Time {
	from, to := now.Add(-24*time.Hour), now.Add(horizon+24*time.Hour)
	const chunk = 60 * 24 * time.Hour
	var out [][2]time.Time
	for start := from; start.Before(to); start = start.Add(chunk) {
		end := start.Add(chunk)
		if end.After(to) {
			end = to
		}
		out = append(out, [2]time.Time{start, end})
	}
	return out
}

// GoogleBusy reads busy time from Google's free/busy API. It implements
// calsource.BusyFetcher.
type GoogleBusy struct {
	P *Provider
	// Calendars are calendar IDs; empty means the primary calendar.
	Calendars []string
	Horizon   time.Duration
	Now       func() time.Time
}

func (g *GoogleBusy) Name() string {
	if len(g.Calendars) == 0 {
		return "Google Calendar (primary)"
	}
	return "Google Calendar (" + strings.Join(g.Calendars, ", ") + ")"
}

func (g *GoogleBusy) FetchBusy(ctx context.Context) ([]schedule.Interval, error) {
	ids := g.Calendars
	if len(ids) == 0 {
		ids = []string{"primary"}
	}
	type item struct {
		ID string `json:"id"`
	}
	items := make([]item, len(ids))
	for i, id := range ids {
		items[i] = item{id}
	}
	now := time.Now
	if g.Now != nil {
		now = g.Now
	}
	var busy []schedule.Interval
	for _, w := range window(now(), g.Horizon) {
		var resp struct {
			Calendars map[string]struct {
				Busy   []struct{ Start, End time.Time }
				Errors []struct{ Reason string }
			}
		}
		req := map[string]any{"timeMin": w[0].UTC().Format(time.RFC3339), "timeMax": w[1].UTC().Format(time.RFC3339), "items": items}
		if _, err := g.P.do(ctx, http.MethodPost, "/calendar/v3/freeBusy", req, &resp); err != nil {
			return nil, err
		}
		for id, cal := range resp.Calendars {
			if len(cal.Errors) > 0 {
				return nil, fmt.Errorf("calendar %s: %s", id, cal.Errors[0].Reason)
			}
			for _, b := range cal.Busy {
				busy = append(busy, schedule.Interval{Start: b.Start, End: b.End})
			}
		}
	}
	return busy, nil
}

// GoogleWriter keeps Bookings in a Google calendar. Event IDs are derived
// from the Booking's UID, so updates and deletes need no stored state.
type GoogleWriter struct {
	P *Provider
	// Calendar is a calendar ID; empty means primary.
	Calendar string
}

// googleEventID maps a UID to Google's ID alphabet (base32hex, lower case).
func googleEventID(uid string) string {
	sum := sha1.Sum([]byte(uid))
	return "hima" + strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}

func (w *GoogleWriter) eventsPath() string {
	cal := w.Calendar
	if cal == "" {
		cal = "primary"
	}
	return "/calendar/v3/calendars/" + url.PathEscape(cal) + "/events"
}

func (w *GoogleWriter) Put(ctx context.Context, _ store.Booking, ev ics.Event) error {
	id := googleEventID(ev.UID)
	body := map[string]any{
		"id":          id,
		"summary":     ev.Summary,
		"description": ev.Notes,
		"location":    ev.Location,
		"start":       map[string]string{"dateTime": ev.Start.UTC().Format(time.RFC3339)},
		"end":         map[string]string{"dateTime": ev.End.UTC().Format(time.RFC3339)},
		"status":      "confirmed",
	}
	status, err := w.P.do(ctx, http.MethodPut, w.eventsPath()+"/"+id, body, nil)
	if status == http.StatusNotFound {
		_, err = w.P.do(ctx, http.MethodPost, w.eventsPath(), body, nil)
	}
	return err
}

func (w *GoogleWriter) Delete(ctx context.Context, _ store.Booking, ev ics.Event) error {
	status, err := w.P.do(ctx, http.MethodDelete, w.eventsPath()+"/"+googleEventID(ev.UID), nil, nil)
	if status == http.StatusNotFound || status == http.StatusGone {
		return nil
	}
	return err
}
