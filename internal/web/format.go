package web

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// commonZones are offered in the Guest's time zone picker; the detected or
// current zone is always added.
var commonZones = []string{
	"UTC",
	"Europe/Lisbon", "Europe/London", "Europe/Dublin", "Europe/Madrid", "Europe/Paris", "Europe/Berlin",
	"Europe/Amsterdam", "Europe/Copenhagen", "Europe/Rome", "Europe/Warsaw", "Europe/Prague", "Europe/Stockholm",
	"Europe/Athens", "Europe/Kyiv", "Europe/Istanbul", "Europe/Moscow",
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

// zoneCity names a zone for people: "Europe/Copenhagen" → "Copenhagen".
func zoneCity(loc *time.Location) string {
	name := loc.String()
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.ReplaceAll(name, "_", " ")
}

// offsetLabel is a zone's UTC offset at t, e.g. "UTC+1" or "UTC+5:30".
func offsetLabel(t time.Time, loc *time.Location) string {
	_, off := t.In(loc).Zone()
	if off == 0 {
		return "UTC"
	}
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	h, m := off/3600, off%3600/60
	if m == 0 {
		return fmt.Sprintf("UTC%s%d", sign, h)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
}

// tzLabel is "Copenhagen (UTC+1)", or just "UTC".
func tzLabel(t time.Time, loc *time.Location) string {
	city, off := zoneCity(loc), offsetLabel(t, loc)
	if city == off {
		return city
	}
	return city + " (" + off + ")"
}

func dayLong(t time.Time) string    { return t.Format("Monday 2 January") }
func dayShort(t time.Time) string   { return t.Format("Mon 2 Jan") }
func dayHeading(t time.Time) string { return t.Format("Mon 2 January") }
func clockLabel(t time.Time) string { return t.Format("15:04") }
func dateShort(t time.Time) string  { return t.Format("2 Jan") }

// timeRange is "14:00 – 14:30" in loc.
func timeRange(start, end time.Time, loc *time.Location) string {
	return clockLabel(start.In(loc)) + " – " + clockLabel(end.In(loc))
}

// whenLabel formats a time range in loc, e.g. "Mon 26 Oct, 10:00–10:30".
func whenLabel(start, end time.Time, loc *time.Location) string {
	return schedule.FormatRange(start, end, loc)
}

// ownerTime is the "15:00 – 15:30 in Nikolai's time" hint, empty when the
// Guest's zone matches the Owner's at that moment.
func (s *Server) ownerTime(start, end time.Time, guest *time.Location) string {
	owner := s.svc.Availability.Location
	_, gOff := start.In(guest).Zone()
	_, oOff := start.In(owner).Zone()
	if gOff == oOff {
		return ""
	}
	day := ""
	if start.In(guest).YearDay() != start.In(owner).YearDay() {
		day = dayShort(start.In(owner)) + ", "
	}
	return day + timeRange(start, end, owner) + " in " + s.cfg.Owner.Name + "'s time"
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

// minutesLabel is "90 min": how durations a Guest picks are named.
func minutesLabel(d time.Duration) string { return strconv.Itoa(int(d/time.Minute)) + " min" }

// durationsLabel is "60 · 90 min".
func durationsLabel(ds []time.Duration) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = strconv.Itoa(int(d / time.Minute))
	}
	return strings.Join(parts, " · ") + " min"
}

func minutes(d time.Duration) string { return strconv.Itoa(int(d / time.Minute)) }

// calendarDays counts calendar days from a to b in loc.
func calendarDays(a, b time.Time, loc *time.Location) int {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	da := time.Date(ay, am, ad, 12, 0, 0, 0, time.UTC)
	db := time.Date(by, bm, bd, 12, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}

// relativeDay is "today", "tomorrow" or "in 3 days".
func relativeDay(now, t time.Time, loc *time.Location) string {
	switch n := calendarDays(now, t, loc); {
	case n <= 0:
		return "today"
	case n == 1:
		return "tomorrow"
	default:
		return fmt.Sprintf("in %d days", n)
	}
}

// untilLabel is "in 6 days", "in 5 h" or "in 20 min" for a future time.
func untilLabel(now, t time.Time) string {
	d := t.Sub(now)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("in %d min", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("in %d days", int(d.Hours()/24))
	}
}

// agoLabel is "just now", "3 min ago", "1 h 12 min ago" or "2 days ago".
func agoLabel(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d h ago", h)
		}
		return fmt.Sprintf("%d h %d min ago", h, m)
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// kindLabel names a Location kind.
func kindLabel(kind string) string {
	switch kind {
	case store.LocationVideo:
		return "Video call"
	case store.LocationPhone:
		return "Phone call"
	case store.LocationPlace:
		return "In person"
	}
	return ""
}

// locationTeaser is what a Guest sees before booking: the kind for calls,
// the place itself for in-person meetings.
func locationTeaser(kind, value string) string {
	if (kind == store.LocationPlace || kind == store.LocationNone) && value != "" {
		return value
	}
	return kindLabel(kind)
}

// locationSummary is how the admin lists a Location.
func locationSummary(kind, value string) string {
	switch {
	case kind == store.LocationPlace && value != "":
		return value
	case kind != "":
		return kindLabel(kind)
	case value != "":
		return value
	}
	return "No location"
}

func isLink(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// withQuery returns path with q encoded, or path alone if q is empty.
func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

var hexColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
