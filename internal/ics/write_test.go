package ics

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	start := time.Date(2026, 10, 26, 10, 0, 0, 0, lisbon)
	err := Write(&buf, "hima", Event{
		UID: "booking-1@hima", Sequence: 2, Start: start, End: start.Add(30 * time.Minute),
		Summary: "Alex ↔ Nikolai: catch-up", Location: "Café, Rua X", Notes: "Bring the map",
		URL: "https://hima.example.com/b/tok", Stamp: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"UID:booking-1@hima", "SEQUENCE:2", "DTSTART:20261026T100000Z", "STATUS:CONFIRMED", "LOCATION:Café\\, Rua X"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	cal, err := Parse(strings.NewReader(out), Options{Location: lisbon})
	if err != nil {
		t.Fatal(err)
	}
	if got := cal.Busy(start, start.Add(time.Hour), false); len(got) != 1 {
		t.Errorf("round trip busy = %v", got)
	}
}

func TestWriteCancelled(t *testing.T) {
	var buf bytes.Buffer
	start := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	Write(&buf, "", Event{UID: "x", Start: start, End: start.Add(time.Hour), Summary: "s", Cancelled: true, Stamp: start})
	if !strings.Contains(buf.String(), "STATUS:CANCELLED") {
		t.Errorf("not cancelled:\n%s", buf.String())
	}
}
