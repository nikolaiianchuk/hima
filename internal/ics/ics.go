// Package ics turns an iCalendar feed (a Calendar source) into Busy blocks.
//
// It expands recurring events (RRULE, RDATE, EXDATE) and applies moved or
// cancelled instances (RECURRENCE-ID). Events marked free (TRANSP:TRANSPARENT),
// cancelled, or declined by the Owner never block; all-day events block only
// when asked to.
package ics

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"

	"github.com/nikolaiianchuk/hima/internal/schedule"
)

// Calendar is a parsed feed, ready to be queried for Busy blocks.
type Calendar struct {
	series []series
	// Skipped counts events that could not be understood and were ignored.
	Skipped int
}

// series is one event: a single occurrence or a recurrence.
type series struct {
	start  time.Time
	dur    time.Duration
	allDay bool
	days   int // length of an all-day event in calendar days

	rule    *rrule.RRule // nil for a single occurrence
	rdates  []time.Time
	exdates map[int64]bool // excluded occurrence starts, unix seconds
}

// Options describe the Owner to a parser.
type Options struct {
	// Location is the Owner's time zone, used for floating times and all-day
	// dates.
	Location *time.Location
	// OwnerEmails identify the Owner among an event's attendees, so events
	// they declined can be ignored. Matching is case-insensitive.
	OwnerEmails []string
	// IgnoreUIDDomain skips events whose UID ends in "@"+IgnoreUIDDomain:
	// hima's own Bookings written back to the Owner's calendar, which are
	// already Busy blocks from the database.
	IgnoreUIDDomain string
}

// Parse reads a feed.
func Parse(r io.Reader, opts Options) (*Calendar, error) {
	ic, err := ical.NewDecoder(r).Decode()
	if err != nil {
		return nil, fmt.Errorf("ics: %w", err)
	}
	return ParseCalendars([]*ical.Calendar{ic}, opts), nil
}

// ParseCalendars combines already-decoded calendars, e.g. one per CalDAV
// object, into one Calendar.
func ParseCalendars(ics []*ical.Calendar, opts Options) *Calendar {
	loc := opts.Location
	owner := make(map[string]bool, len(opts.OwnerEmails))
	for _, e := range opts.OwnerEmails {
		owner[strings.ToLower(strings.TrimSpace(e))] = true
	}
	ignoreSuffix := ""
	if opts.IgnoreUIDDomain != "" {
		ignoreSuffix = "@" + strings.ToLower(opts.IgnoreUIDDomain)
	}
	var events []ical.Event
	for _, ic := range ics {
		events = append(events, ic.Events()...)
	}

	type override struct {
		uid string
		at  time.Time
	}
	var (
		cal       Calendar
		overrides []override
		masters   = map[string]int{} // UID -> index of its recurring series
	)
	for _, ev := range events {
		uid, _ := ev.Props.Text(ical.PropUID)
		if ignoreSuffix != "" && strings.HasSuffix(strings.ToLower(uid), ignoreSuffix) {
			continue
		}
		isOverride := false
		if rid := ev.Props.Get(ical.PropRecurrenceID); rid != nil {
			at, err := dateTime(rid, loc)
			if err != nil {
				cal.Skipped++
				continue
			}
			overrides = append(overrides, override{uid: uid, at: at})
			isOverride = true
		}
		if !blocks(ev, owner) {
			continue
		}
		s, ok, err := toSeries(ev, loc)
		if err != nil {
			cal.Skipped++
			continue
		}
		if !ok {
			continue
		}
		if isOverride {
			s.rule, s.rdates = nil, nil
		}
		if s.rule != nil || len(s.rdates) > 0 {
			masters[uid] = len(cal.series)
		}
		cal.series = append(cal.series, s)
	}
	// A moved or cancelled instance replaces its slot in the recurrence.
	for _, o := range overrides {
		if i, ok := masters[o.uid]; ok {
			cal.series[i].exdates[o.at.Unix()] = true
		}
	}
	return &cal
}

// Busy returns the Busy blocks overlapping [from, to).
func (c *Calendar) Busy(from, to time.Time, allDayBlocks bool) []schedule.Interval {
	window := schedule.Interval{Start: from, End: to}
	var out []schedule.Interval
	for _, s := range c.series {
		if s.allDay && !allDayBlocks {
			continue
		}
		for _, start := range s.startsBetween(from, to) {
			iv := schedule.Interval{Start: start, End: s.end(start)}
			if iv.Overlaps(window) {
				out = append(out, iv)
			}
		}
	}
	return out
}

func (s series) end(start time.Time) time.Time {
	if s.allDay {
		return start.AddDate(0, 0, s.days)
	}
	return start.Add(s.dur)
}

// startsBetween returns occurrence starts that may overlap [from, to).
func (s series) startsBetween(from, to time.Time) []time.Time {
	if s.rule == nil && len(s.rdates) == 0 {
		return []time.Time{s.start}
	}
	// An occurrence starting up to one duration before from still overlaps;
	// the extra hour covers all-day events spanning a DST change.
	after := from.Add(-s.dur - time.Hour)
	var starts []time.Time
	if s.rule != nil {
		starts = s.rule.Between(after, to, true)
	}
	for _, d := range s.rdates {
		if !d.Before(after) && d.Before(to) {
			starts = append(starts, d)
		}
	}
	kept := starts[:0]
	for _, st := range starts {
		if !s.exdates[st.Unix()] {
			kept = append(kept, st)
		}
	}
	return kept
}

func blocks(ev ical.Event, owner map[string]bool) bool {
	if transp, _ := ev.Props.Text(ical.PropTransparency); strings.EqualFold(transp, "TRANSPARENT") {
		return false
	}
	if status, _ := ev.Props.Text(ical.PropStatus); strings.EqualFold(status, "CANCELLED") {
		return false
	}
	return !ownerDeclined(ev, owner)
}

// ownerDeclined reports whether the Owner appears among the attendees and
// declined under every address they appear with. Accepted, tentative and
// unanswered invitations all block.
func ownerDeclined(ev ical.Event, owner map[string]bool) bool {
	if len(owner) == 0 {
		return false
	}
	matched := false
	for _, a := range ev.Props[ical.PropAttendee] {
		if !owner[attendeeEmail(a)] {
			continue
		}
		matched = true
		if !strings.EqualFold(a.Params.Get(ical.ParamParticipationStatus), "DECLINED") {
			return false
		}
	}
	return matched
}

// attendeeEmail returns the lower-cased address of an ATTENDEE, from its
// mailto: value or, failing that, its EMAIL parameter.
func attendeeEmail(a ical.Prop) string {
	v := a.Value
	if len(v) > len("mailto:") && strings.EqualFold(v[:len("mailto:")], "mailto:") {
		return strings.ToLower(v[len("mailto:"):])
	}
	return strings.ToLower(a.Params.Get("EMAIL"))
}

// toSeries converts an event. ok is false for events that occupy no time.
func toSeries(ev ical.Event, loc *time.Location) (s series, ok bool, err error) {
	startProp := ev.Props.Get(ical.PropDateTimeStart)
	if startProp == nil {
		return s, false, fmt.Errorf("missing DTSTART")
	}
	if s.start, err = dateTime(startProp, loc); err != nil {
		return s, false, err
	}
	s.allDay = isDate(startProp)

	var end time.Time
	switch {
	case ev.Props.Get(ical.PropDateTimeEnd) != nil:
		if end, err = dateTime(ev.Props.Get(ical.PropDateTimeEnd), loc); err != nil {
			return s, false, err
		}
	case ev.Props.Get(ical.PropDuration) != nil:
		d, err := ev.Props.Get(ical.PropDuration).Duration()
		if err != nil {
			return s, false, err
		}
		end = s.start.Add(d)
	case s.allDay:
		end = s.start.AddDate(0, 0, 1)
	default:
		end = s.start
	}
	s.dur = end.Sub(s.start)
	if s.allDay {
		// Round so a day shortened or lengthened by DST still counts as one.
		s.days = int((s.dur + 12*time.Hour) / (24 * time.Hour))
		if s.days < 1 {
			s.days = 1
		}
	} else if s.dur <= 0 {
		return s, false, nil
	}

	opt, err := ev.Props.RecurrenceRule()
	if err != nil {
		return s, false, err
	}
	if opt != nil {
		opt.Dtstart = s.start
		if s.rule, err = rrule.NewRRule(*opt); err != nil {
			return s, false, err
		}
	}
	if s.rdates, err = dateList(ev.Props[ical.PropRecurrenceDates], loc); err != nil {
		return s, false, err
	}
	exdates, err := dateList(ev.Props[ical.PropExceptionDates], loc)
	if err != nil {
		return s, false, err
	}
	s.exdates = make(map[int64]bool, len(exdates))
	for _, d := range exdates {
		s.exdates[d.Unix()] = true
	}
	return s, true, nil
}

func isDate(p *ical.Prop) bool {
	return p.ValueType() == ical.ValueDate || len(p.Value) == len("20060102")
}

// dateList parses RDATE/EXDATE properties, each of which may hold several
// comma-separated values.
func dateList(props []ical.Prop, loc *time.Location) ([]time.Time, error) {
	var out []time.Time
	for _, p := range props {
		for _, v := range strings.Split(p.Value, ",") {
			single := p
			single.Value = strings.TrimSpace(v)
			t, err := dateTime(&single, loc)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// dateTime parses a DATE or DATE-TIME property. A TZID Go does not know is
// mapped from its Windows name when possible, otherwise treated as floating
// time in loc.
func dateTime(p *ical.Prop, loc *time.Location) (time.Time, error) {
	t, err := p.DateTime(loc)
	if err == nil {
		return t, nil
	}
	tzid := p.Params.Get(ical.PropTimezoneID)
	if tzid == "" {
		return time.Time{}, err
	}
	fallback := loc
	if iana, ok := windowsZones[tzid]; ok {
		if l, lerr := time.LoadLocation(iana); lerr == nil {
			fallback = l
		}
	}
	stripped := *p
	stripped.Params = ical.Params{}
	for k, v := range p.Params {
		if k != ical.PropTimezoneID {
			stripped.Params[k] = v
		}
	}
	return stripped.DateTime(fallback)
}
