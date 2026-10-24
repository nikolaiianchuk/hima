package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/nikolaiianchuk/hima/internal/notify"
)

type collect struct{ kinds []notify.Kind }

func (c *collect) Notify(_ context.Context, ev notify.Event) { c.kinds = append(c.kinds, ev.Kind) }

func TestCalendarWatcherNotifiesOnTransitionsOnly(t *testing.T) {
	var c collect
	watch := calendarWatcher(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), &c)
	boom := errors.New("boom")
	for _, err := range []error{nil, boom, boom, boom, nil, nil, boom} {
		watch(err)
	}
	want := []notify.Kind{notify.CalendarFailing, notify.CalendarRecovered, notify.CalendarFailing}
	if len(c.kinds) != len(want) {
		t.Fatalf("got %v, want %v", c.kinds, want)
	}
	for i := range want {
		if c.kinds[i] != want[i] {
			t.Fatalf("got %v, want %v", c.kinds, want)
		}
	}
}
