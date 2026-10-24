package schedule

import (
	"fmt"
	"time"
)

// FormatRange renders a time range in loc for people, e.g.
// "Mon 26 Oct, 10:00–10:30", or with both days when it spans midnight.
func FormatRange(start, end time.Time, loc *time.Location) string {
	s, e := start.In(loc), end.In(loc)
	const day, clock = "Mon 2 Jan", "15:04"
	sameDay := s.YearDay() == e.YearDay() && s.Year() == e.Year()
	endsAtMidnight := e.Sub(s) < 24*time.Hour && e.Hour() == 0 && e.Minute() == 0
	if sameDay || endsAtMidnight {
		return fmt.Sprintf("%s, %s–%s", s.Format(day), s.Format(clock), e.Format(clock))
	}
	return fmt.Sprintf("%s %s – %s %s", s.Format(day), s.Format(clock), e.Format(day), e.Format(clock))
}
