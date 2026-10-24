package schedule

import (
	"testing"
	"time"
)

var lisbon = mustLoad("Europe/Lisbon")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, lisbon)
	if err != nil {
		panic(err)
	}
	return t
}

func weekdays(r ...DayRange) [7][]DayRange {
	var w [7][]DayRange
	for d := time.Monday; d <= time.Friday; d++ {
		w[d] = r
	}
	return w
}

func base() Availability {
	return Availability{
		Location: lisbon,
		Weekly:   weekdays(DayRange{From: 10 * 60, To: 12 * 60}),
		Horizon:  14 * 24 * time.Hour,
		Step:     30 * time.Minute,
	}
}

func starts(slots []Slot) []string {
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = s.Start.In(lisbon).Format("Mon 01-02 15:04")
	}
	return out
}

func assertStarts(t *testing.T, got []Slot, want ...string) {
	t.Helper()
	g := starts(got)
	if len(g) != len(want) {
		t.Fatalf("got %d slots %v, want %v", len(g), g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("got %v, want %v", g, want)
		}
	}
}

// 2026-10-26 is a Monday.
const monday = "2026-10-26"

func TestSlotsWithinDayRange(t *testing.T) {
	a := base()
	got := a.Slots(Request{
		Now: at("2026-10-25 09:00"), Duration: time.Hour,
		From: at(monday + " 00:00"), To: at(monday + " 23:59"),
	}, nil)
	assertStarts(t, got, "Mon 10-26 10:00", "Mon 10-26 10:30", "Mon 10-26 11:00")
}

func TestSlotsSkipWeekend(t *testing.T) {
	a := base()
	got := a.Slots(Request{
		Now: at("2026-10-24 09:00"), Duration: 2 * time.Hour,
		To: at("2026-10-27 00:00"),
	}, nil)
	assertStarts(t, got, "Mon 10-26 10:00")
}

func TestSlotsMinNotice(t *testing.T) {
	a := base()
	a.MinNotice = 12 * time.Hour
	got := a.Slots(Request{
		Now: at(monday + " 22:30"), Duration: time.Hour,
		To: at("2026-10-28 00:00"),
	}, nil)
	// Tuesday 10:00 is only 11.5h away.
	assertStarts(t, got, "Tue 10-27 10:30", "Tue 10-27 11:00")
}

func TestSlotsHorizon(t *testing.T) {
	a := base()
	a.Horizon = 25 * time.Hour
	got := a.Slots(Request{Now: at(monday + " 10:30"), Duration: time.Hour}, nil)
	assertStarts(t, got, "Mon 10-26 10:30", "Mon 10-26 11:00", "Tue 10-27 10:00", "Tue 10-27 10:30")
}

func TestSlotsBusyAndBuffer(t *testing.T) {
	a := base()
	a.Buffer = 15 * time.Minute
	busy := []Interval{{Start: at(monday + " 11:00"), End: at(monday + " 11:15")}}
	got := a.Slots(Request{
		Now: at("2026-10-25 09:00"), Duration: 30 * time.Minute,
		From: at(monday + " 00:00"), To: at(monday + " 23:59"),
	}, busy)
	// Blocked 10:45–11:30.
	assertStarts(t, got, "Mon 10-26 10:00", "Mon 10-26 11:30")
}

func TestSlotsInviteRangeCannotWiden(t *testing.T) {
	a := base()
	a.Horizon = 27 * time.Hour // window ends Mon 12:00
	got := a.Slots(Request{
		Now: at("2026-10-25 09:00"), Duration: 2 * time.Hour,
		From: at("2026-10-01 00:00"), To: at("2026-12-01 00:00"),
	}, nil)
	assertStarts(t, got, "Mon 10-26 10:00")
}

func TestSlotsFollowWallClockAcrossDST(t *testing.T) {
	// Europe/Lisbon leaves summer time on 2026-10-25.
	a := base()
	got := a.Slots(Request{
		Now: at("2026-10-22 00:00"), Duration: 2 * time.Hour,
		To: at("2026-10-27 00:00"),
	}, nil)
	assertStarts(t, got, "Thu 10-22 10:00", "Fri 10-23 10:00", "Mon 10-26 10:00")
	if thu, mon := got[0].Start.UTC().Hour(), got[2].Start.UTC().Hour(); thu != 9 || mon != 10 {
		t.Errorf("10:00 Lisbon should be 09:00 UTC in summer and 10:00 UTC after; got %d and %d", thu, mon)
	}
}

func TestSlotsEndOfDay(t *testing.T) {
	a := base()
	a.Weekly = weekdays(DayRange{From: 23 * 60, To: 24 * 60})
	got := a.Slots(Request{
		Now: at("2026-10-25 09:00"), Duration: time.Hour,
		To: at("2026-10-27 00:00"),
	}, nil)
	assertStarts(t, got, "Mon 10-26 23:00")
}

func TestMerge(t *testing.T) {
	got := Merge([]Interval{
		{Start: at(monday + " 12:00"), End: at(monday + " 13:00")},
		{Start: at(monday + " 10:00"), End: at(monday + " 11:00")},
		{Start: at(monday + " 11:00"), End: at(monday + " 11:30")},
		{Start: at(monday + " 12:30"), End: at(monday + " 12:45")},
	})
	if len(got) != 2 || !got[0].End.Equal(at(monday+" 11:30")) || !got[1].End.Equal(at(monday+" 13:00")) {
		t.Errorf("Merge = %v", got)
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]Clock{"00:00": 0, "09:30": 570, "24:00": 1440} {
		if got, err := ParseClock(in); err != nil || got != want {
			t.Errorf("ParseClock(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"24:01", "10:60", "ten"} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) should fail", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	a := base()
	a.Weekly[time.Monday] = []DayRange{{From: 600, To: 720}, {From: 700, To: 800}}
	if a.Validate() == nil {
		t.Error("overlapping ranges should be invalid")
	}
	if base().Validate() != nil {
		t.Error("base should be valid")
	}
}
