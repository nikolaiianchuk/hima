package schedule

import (
	"errors"
	"fmt"
	"time"
)

// Clock is a wall-clock time of day in minutes since midnight; 24:00 is
// allowed as the end of a day.
type Clock int

func ParseClock(s string) (Clock, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || m < 0 || m > 59 || h*60+m > 24*60 {
		return 0, fmt.Errorf("invalid time of day %q (want HH:MM)", s)
	}
	return Clock(h*60 + m), nil
}

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c/60, c%60) }

// DayRange is a bookable stretch of one day, e.g. 10:00–18:00.
type DayRange struct {
	From, To Clock
}

// Availability is the Owner's standing rules for when they can be booked.
// Hours are wall-clock time in Location, so they follow DST.
type Availability struct {
	Location *time.Location
	// Weekly is indexed by time.Weekday; ranges must be sorted and disjoint.
	Weekly [7][]DayRange
	// Buffer is kept free on both sides of every Busy block.
	Buffer time.Duration
	// MinNotice is how soon from now the earliest Slot may start.
	MinNotice time.Duration
	// Horizon is how far from now the latest Slot may end.
	Horizon time.Duration
	// Step is the spacing between candidate Slot starts within a DayRange.
	Step time.Duration
}

func (a Availability) Validate() error {
	if a.Location == nil {
		return errors.New("availability: time zone is required")
	}
	if a.Step <= 0 {
		return errors.New("availability: step must be positive")
	}
	if a.Horizon <= 0 {
		return errors.New("availability: horizon must be positive")
	}
	if a.Buffer < 0 || a.MinNotice < 0 {
		return errors.New("availability: buffer and minimum notice cannot be negative")
	}
	for wd, ranges := range a.Weekly {
		var prevEnd Clock
		for i, r := range ranges {
			if r.From >= r.To {
				return fmt.Errorf("availability: %s %s-%s is empty", time.Weekday(wd), r.From, r.To)
			}
			if i > 0 && r.From < prevEnd {
				return fmt.Errorf("availability: %s ranges overlap or are unsorted", time.Weekday(wd))
			}
			prevEnd = r.To
		}
	}
	return nil
}
