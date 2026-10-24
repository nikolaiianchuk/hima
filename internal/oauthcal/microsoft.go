package oauthcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// NewMicrosoft configures Microsoft 365 / Outlook.com. tenant is "common"
// for any account, or a directory ID to restrict sign-in.
func NewMicrosoft(clientID, clientSecret, tenant, redirectURL string, st *store.Store, write bool) *Provider {
	if tenant == "" {
		tenant = "common"
	}
	cal := "Calendars.Read"
	if write {
		cal = "Calendars.ReadWrite"
	}
	p := &Provider{
		ID:    "microsoft",
		Title: "Microsoft 365",
		OAuth: &oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
			Endpoint: endpoints.AzureAD(tenant),
			Scopes:   []string{"offline_access", "User.Read", cal},
		},
		Store:   st,
		APIBase: "https://graph.microsoft.com/v1.0",
	}
	p.fetchAccount = func(ctx context.Context, c *http.Client) (string, error) {
		resp, err := c.Get(p.APIBase + "/me")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var me struct{ Mail, UserPrincipalName string }
		if err := decodeOK(resp, &me); err != nil {
			return "", err
		}
		if me.Mail != "" {
			return me.Mail, nil
		}
		return me.UserPrincipalName, nil
	}
	return p
}

const graphTime = "2006-01-02T15:04:05.9999999"

type graphDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

func toGraph(t time.Time) graphDateTime {
	return graphDateTime{DateTime: t.UTC().Format("2006-01-02T15:04:05"), TimeZone: "UTC"}
}

func (g graphDateTime) time() (time.Time, error) {
	loc := time.UTC
	if g.TimeZone != "" && g.TimeZone != "UTC" {
		l, err := time.LoadLocation(g.TimeZone)
		if err != nil {
			return time.Time{}, err
		}
		loc = l
	}
	return time.ParseInLocation(graphTime, g.DateTime, loc)
}

// MicrosoftBusy reads the connected mailbox's busy time with getSchedule.
// It implements calsource.BusyFetcher.
type MicrosoftBusy struct {
	P       *Provider
	Horizon time.Duration
	Now     func() time.Time
}

func (m *MicrosoftBusy) Name() string { return "Microsoft 365 calendar" }

func (m *MicrosoftBusy) FetchBusy(ctx context.Context) ([]schedule.Interval, error) {
	account, err := m.P.Account(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	var busy []schedule.Interval
	for _, w := range window(now(), m.Horizon) {
		var resp struct {
			Value []struct {
				Error *struct{ Message string }
				// Status is free, tentative, busy, oof, workingElsewhere or unknown.
				ScheduleItems []struct {
					Status     string
					Start, End graphDateTime
				}
			}
		}
		req := map[string]any{
			"schedules":                []string{account},
			"startTime":                toGraph(w[0]),
			"endTime":                  toGraph(w[1]),
			"availabilityViewInterval": 30,
		}
		if _, err := m.P.do(ctx, http.MethodPost, "/me/calendar/getSchedule", req, &resp, "Prefer", `outlook.timezone="UTC"`); err != nil {
			return nil, err
		}
		for _, v := range resp.Value {
			if v.Error != nil {
				return nil, fmt.Errorf("getSchedule: %s", v.Error.Message)
			}
			for _, it := range v.ScheduleItems {
				switch strings.ToLower(it.Status) {
				case "busy", "tentative", "oof":
				default:
					continue
				}
				start, err := it.Start.time()
				if err != nil {
					return nil, err
				}
				end, err := it.End.time()
				if err != nil {
					return nil, err
				}
				busy = append(busy, schedule.Interval{Start: start, End: end})
			}
		}
	}
	return busy, nil
}

// MicrosoftWriter keeps Bookings in the mailbox's default calendar. Graph
// assigns event IDs, so they are kept in the store.
type MicrosoftWriter struct {
	P *Provider
}

func (w *MicrosoftWriter) Put(ctx context.Context, b store.Booking, ev ics.Event) error {
	body := map[string]any{
		"subject":       ev.Summary,
		"body":          map[string]string{"contentType": "text", "content": ev.Notes},
		"start":         toGraph(ev.Start),
		"end":           toGraph(ev.End),
		"location":      map[string]string{"displayName": ev.Location},
		"showAs":        "busy",
		"transactionId": ev.UID, // makes a retried create idempotent
	}
	id, err := w.P.Store.ExternalID(ctx, b.ID, w.P.ID)
	if err != nil {
		return err
	}
	if id != "" {
		status, err := w.P.do(ctx, http.MethodPatch, "/me/events/"+id, body, nil)
		if status != http.StatusNotFound {
			return err
		}
	}
	var created struct{ ID string }
	if _, err := w.P.do(ctx, http.MethodPost, "/me/events", body, &created); err != nil {
		return err
	}
	return w.P.Store.SetExternalID(ctx, b.ID, w.P.ID, created.ID)
}

func (w *MicrosoftWriter) Delete(ctx context.Context, b store.Booking, _ ics.Event) error {
	id, err := w.P.Store.ExternalID(ctx, b.ID, w.P.ID)
	if err != nil || id == "" {
		return err
	}
	status, err := w.P.do(ctx, http.MethodDelete, "/me/events/"+id, nil, nil)
	if err != nil && status != http.StatusNotFound {
		return err
	}
	return w.P.Store.DeleteExternalID(ctx, b.ID, w.P.ID)
}

func decodeOK(resp *http.Response, out any) error {
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
