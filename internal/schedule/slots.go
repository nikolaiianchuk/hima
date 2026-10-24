package schedule

import (
	"sort"
	"time"
)

// Request describes what a Guest is choosing from: the Invite's limits and
// the moment of asking.
type Request struct {
	Now      time.Time
	Duration time.Duration
	// From and To narrow Availability to the Invite's date range; zero means
	// unbounded. An Invite can only narrow, never widen (see CONTEXT.md).
	From, To time.Time
}

// Slot is a time a Guest may book.
type Slot = Interval

// Slots lists bookable Slots in chronological order. busy may be unsorted
// and overlapping.
func (a Availability) Slots(req Request, busy []Interval) []Slot {
	if req.Duration <= 0 {
		return nil
	}
	window := Interval{Start: req.Now.Add(a.MinNotice), End: req.Now.Add(a.Horizon)}
	if !req.From.IsZero() && req.From.After(window.Start) {
		window.Start = req.From
	}
	if !req.To.IsZero() && req.To.Before(window.End) {
		window.End = req.To
	}
	if !window.Start.Before(window.End) {
		return nil
	}

	blocked := make([]Interval, len(busy))
	for i, b := range busy {
		blocked[i] = Interval{Start: b.Start.Add(-a.Buffer), End: b.End.Add(a.Buffer)}
	}
	blocked = Merge(blocked)

	var slots []Slot
	start := window.Start.In(a.Location)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, a.Location)
	for ; day.Before(window.End); day = day.AddDate(0, 0, 1) {
		for _, r := range a.Weekly[day.Weekday()] {
			rangeStart := atClock(day, r.From)
			rangeEnd := atClock(day, r.To)
			for t := rangeStart; !t.Add(req.Duration).After(rangeEnd); t = t.Add(a.Step) {
				s := Slot{Start: t, End: t.Add(req.Duration)}
				if s.Start.Before(window.Start) || s.End.After(window.End) {
					continue
				}
				if !overlapsAny(blocked, s) {
					slots = append(slots, s)
				}
			}
		}
	}
	return slots
}

// atClock returns c on the calendar day of midnight, in midnight's location.
// Times skipped by a DST jump are normalised forward by time.Date.
func atClock(midnight time.Time, c Clock) time.Time {
	return time.Date(midnight.Year(), midnight.Month(), midnight.Day(), int(c)/60, int(c)%60, 0, 0, midnight.Location())
}

// overlapsAny reports whether s overlaps any of the sorted, merged ivs.
func overlapsAny(ivs []Interval, s Interval) bool {
	i := sort.Search(len(ivs), func(i int) bool { return ivs[i].End.After(s.Start) })
	return i < len(ivs) && ivs[i].Start.Before(s.End)
}
