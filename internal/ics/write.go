package ics

import (
	"io"
	"strconv"
	"time"

	"github.com/emersion/go-ical"
)

// Event is a Booking as it appears in a published calendar file.
type Event struct {
	UID       string
	Sequence  int
	Start     time.Time
	End       time.Time
	Summary   string
	Location  string
	Notes     string
	URL       string
	Cancelled bool
	Stamp     time.Time
}

// Write encodes events as an iCalendar file.
func Write(w io.Writer, name string, events ...Event) error {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//hima//hima//EN")
	cal.Props.SetText(ical.PropCalendarScale, "GREGORIAN")
	if name != "" {
		cal.Props.SetText("X-WR-CALNAME", name)
	}
	for _, e := range events {
		ev := ical.NewEvent()
		ev.Props.SetText(ical.PropUID, e.UID)
		ev.Props.SetDateTime(ical.PropDateTimeStamp, e.Stamp.UTC())
		ev.Props.SetDateTime(ical.PropDateTimeStart, e.Start.UTC())
		ev.Props.SetDateTime(ical.PropDateTimeEnd, e.End.UTC())
		ev.Props.SetText(ical.PropSummary, e.Summary)
		seq := ical.NewProp(ical.PropSequence)
		seq.SetValueType(ical.ValueInt)
		seq.Value = strconv.Itoa(e.Sequence)
		ev.Props.Set(seq)
		if e.Location != "" {
			ev.Props.SetText(ical.PropLocation, e.Location)
		}
		if e.Notes != "" {
			ev.Props.SetText(ical.PropDescription, e.Notes)
		}
		if e.URL != "" {
			u := ical.NewProp(ical.PropURL)
			u.SetValueType(ical.ValueURI)
			u.Value = e.URL
			ev.Props.Set(u)
		}
		status := ical.EventConfirmed
		if e.Cancelled {
			status = ical.EventCancelled
		}
		ev.SetStatus(status)
		cal.Children = append(cal.Children, ev.Component)
	}
	return ical.NewEncoder(w).Encode(cal)
}
