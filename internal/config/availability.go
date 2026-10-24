package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/schedule"
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
func (c *Config) Sources() []calsource.Source {
	out := make([]calsource.Source, len(c.Calendars))
	for i, cc := range c.Calendars {
		out[i] = calsource.Source{URL: cc.URL, AllDayBlocks: cc.AllDayBlocks}
	}
	return out
}
