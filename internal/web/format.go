package web

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// commonZones are offered in the Guest's time zone picker; the detected or
// current zone is always added.
var commonZones = []string{
	"UTC",
	"Europe/Lisbon", "Europe/London", "Europe/Dublin", "Europe/Madrid", "Europe/Paris", "Europe/Berlin",
	"Europe/Amsterdam", "Europe/Rome", "Europe/Warsaw", "Europe/Prague", "Europe/Stockholm", "Europe/Athens",
	"Europe/Kyiv", "Europe/Istanbul", "Europe/Moscow",
	"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles", "America/Toronto",
	"America/Mexico_City", "America/Sao_Paulo", "America/Buenos_Aires",
	"Asia/Dubai", "Asia/Kolkata", "Asia/Bangkok", "Asia/Singapore", "Asia/Shanghai", "Asia/Tokyo", "Asia/Seoul",
	"Australia/Sydney", "Pacific/Auckland",
}

func zoneChoices(current string) []string {
	if slices.Contains(commonZones, current) {
		return commonZones
	}
	return append([]string{current}, commonZones...)
}

// guestZone returns the zone named by ?tz=, falling back to the Owner's.
func (s *Server) guestZone(q url.Values) *time.Location {
	if name := q.Get("tz"); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return s.svc.Availability.Location
}

func dayLabel(t time.Time) string   { return t.Format("Mon 2 Jan") }
func clockLabel(t time.Time) string { return t.Format("15:04") }

// whenLabel formats a time range in loc, e.g. "Mon 26 Oct, 10:00–10:30".
func whenLabel(start, end time.Time, loc *time.Location) string {
	s, e := start.In(loc), end.In(loc)
	if s.YearDay() == e.YearDay() || e.Sub(s) < 24*time.Hour && e.Hour() == 0 && e.Minute() == 0 {
		return fmt.Sprintf("%s, %s–%s", dayLabel(s), clockLabel(s), clockLabel(e))
	}
	return fmt.Sprintf("%s %s – %s %s", dayLabel(s), clockLabel(s), dayLabel(e), clockLabel(e))
}

// timeRangeLabel is whenLabel without the day when the range stays within
// one day, e.g. "10:00–11:00".
func timeRangeLabel(start, end time.Time, loc *time.Location) string {
	s, e := start.In(loc), end.In(loc)
	if s.YearDay() == e.YearDay() {
		return clockLabel(s) + "–" + clockLabel(e)
	}
	return whenLabel(start, end, loc)
}

// ownerWhen is the "(… Nikolai's time)" hint, empty when zones agree.
func (s *Server) ownerWhen(start, end time.Time, guest *time.Location) string {
	owner := s.svc.Availability.Location
	_, gOff := start.In(guest).Zone()
	_, oOff := start.In(owner).Zone()
	if gOff == oOff {
		return ""
	}
	return fmt.Sprintf("%s in %s's time (%s)", whenLabel(start, end, owner), s.cfg.Owner.Name, owner)
}

func durationLabel(d time.Duration) string {
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d h", h)
	default:
		return fmt.Sprintf("%d h %d min", h, m)
	}
}

func minutes(d time.Duration) string { return strconv.Itoa(int(d / time.Minute)) }

// withQuery returns path with q encoded, or path alone if q is empty.
func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
