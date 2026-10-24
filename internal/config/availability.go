package config

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nikolaiianchuk/hima/internal/caldav"
	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/oauthcal"
	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// AvailabilityConfig is the YAML form of schedule.Availability.
type AvailabilityConfig struct {
	Timezone string `yaml:"timezone"`
	// Weekly maps mon..sun to ranges like "10:00-18:00".
	Weekly      map[string][]string `yaml:"weekly"`
	Buffer      time.Duration       `yaml:"buffer"`
	MinNotice   time.Duration       `yaml:"min_notice"`
	HorizonDays int                 `yaml:"horizon_days"`
	Step        time.Duration       `yaml:"step"`
	// MaxBookingsPerDay caps Bookings starting on one day; 0 means no cap.
	MaxBookingsPerDay int `yaml:"max_bookings_per_day"`
}

type CalendarConfig struct {
	URL          string `yaml:"url"`
	AllDayBlocks bool   `yaml:"all_day_blocks"`
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func (a *AvailabilityConfig) applyDefaults() {
	if a.Timezone == "" {
		a.Timezone = "UTC"
	}
	if a.Weekly == nil {
		a.Weekly = map[string][]string{}
		for _, d := range []string{"mon", "tue", "wed", "thu", "fri"} {
			a.Weekly[d] = []string{"10:00-18:00"}
		}
	}
	if a.HorizonDays == 0 {
		a.HorizonDays = 30
	}
	if a.Step == 0 {
		a.Step = 30 * time.Minute
	}
}

// Availability converts and validates the configured Availability.
func (c *Config) Availability() (schedule.Availability, error) {
	ac := c.AvailabilityConfig
	loc, err := time.LoadLocation(ac.Timezone)
	if err != nil {
		return schedule.Availability{}, fmt.Errorf("availability.timezone: %w", err)
	}
	a := schedule.Availability{
		Location:  loc,
		Buffer:    ac.Buffer,
		MinNotice: ac.MinNotice,
		Horizon:   time.Duration(ac.HorizonDays) * 24 * time.Hour,
		Step:      ac.Step,
		MaxPerDay: ac.MaxBookingsPerDay,
	}
	for name, ranges := range ac.Weekly {
		wd, ok := weekdayNames[strings.ToLower(name)]
		if !ok {
			return a, fmt.Errorf("availability.weekly: unknown day %q (use mon..sun)", name)
		}
		for _, r := range ranges {
			from, to, ok := strings.Cut(r, "-")
			if !ok {
				return a, fmt.Errorf("availability.weekly.%s: %q is not HH:MM-HH:MM", name, r)
			}
			f, err := schedule.ParseClock(strings.TrimSpace(from))
			if err != nil {
				return a, fmt.Errorf("availability.weekly.%s: %w", name, err)
			}
			t, err := schedule.ParseClock(strings.TrimSpace(to))
			if err != nil {
				return a, fmt.Errorf("availability.weekly.%s: %w", name, err)
			}
			a.Weekly[wd] = append(a.Weekly[wd], schedule.DayRange{From: f, To: t})
		}
	}
	return a, a.Validate()
}

// Sources returns the configured Calendar sources.
// Sources returns the configured Calendar sources: every ICS URL, plus the
// CalDAV account's calendars when acc is set and reading isn't turned off.
func (c *Config) Sources(acc *caldav.Account, horizon time.Duration) []calsource.Source {
	client := &http.Client{Timeout: 30 * time.Second}
	var out []calsource.Source
	for _, cc := range c.Calendars {
		out = append(out, calsource.Source{Fetcher: &calsource.ICSFeed{URL: cc.URL, Client: client}, AllDayBlocks: cc.AllDayBlocks})
	}
	if acc != nil && (c.CalDAV.Read == nil || len(c.CalDAV.Read) > 0) {
		out = append(out, calsource.Source{
			Fetcher:      &caldav.Source{Account: acc, Calendars: c.CalDAV.Read, Horizon: horizon},
			AllDayBlocks: c.CalDAV.AllDayBlocks,
		})
	}
	return out
}

// OAuthProviders returns the configured Google and Microsoft connections.
func (c *Config) OAuthProviders(st *store.Store) (google, microsoft *oauthcal.Provider) {
	redirect := func(id string) string { return c.BaseURL + "/admin/oauth/" + id + "/callback" }
	if g := c.Google; g.ClientID != "" {
		google = oauthcal.NewGoogle(g.ClientID, g.ClientSecret, redirect("google"), st, g.Write != "")
	}
	if m := c.Microsoft; m.ClientID != "" {
		microsoft = oauthcal.NewMicrosoft(m.ClientID, m.ClientSecret, m.Tenant, redirect("microsoft"), st, m.Write)
	}
	return google, microsoft
}

// OAuthSources returns busy sources for connected providers that read.
func (c *Config) OAuthSources(google, microsoft *oauthcal.Provider, horizon time.Duration) []calsource.Source {
	var out []calsource.Source
	if google != nil && (c.Google.Read == nil || len(c.Google.Read) > 0) {
		out = append(out, calsource.Source{Busy: &oauthcal.GoogleBusy{P: google, Calendars: c.Google.Read, Horizon: horizon}})
	}
	if microsoft != nil && (c.Microsoft.Read == nil || *c.Microsoft.Read) {
		out = append(out, calsource.Source{Busy: &oauthcal.MicrosoftBusy{P: microsoft, Horizon: horizon}})
	}
	return out
}

// CalDAVAccount returns the configured CalDAV account, or nil.
func (c *Config) CalDAVAccount() *caldav.Account {
	if c.CalDAV.URL == "" {
		return nil
	}
	return &caldav.Account{URL: c.CalDAV.URL, Username: c.CalDAV.Username, Password: c.CalDAV.Password}
}
