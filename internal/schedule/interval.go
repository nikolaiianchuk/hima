// Package schedule decides which Slots a Guest may book, given the Owner's
// Availability, an Invite's limits and the Owner's Busy blocks.
package schedule

import (
	"sort"
	"time"
)

// Interval is a half-open time range [Start, End). Busy blocks are Intervals.
type Interval struct {
	Start, End time.Time
}

func (i Interval) Overlaps(o Interval) bool {
	return i.Start.Before(o.End) && o.Start.Before(i.End)
}

// Merge returns the union of ivs as sorted, non-overlapping Intervals.
// Touching Intervals are joined.
func Merge(ivs []Interval) []Interval {
	if len(ivs) == 0 {
		return nil
	}
	sorted := append([]Interval(nil), ivs...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].Start.Before(sorted[b].Start) })
	out := []Interval{sorted[0]}
	for _, iv := range sorted[1:] {
		last := &out[len(out)-1]
		if !iv.Start.After(last.End) {
			if iv.End.After(last.End) {
				last.End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}
