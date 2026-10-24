package booking

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/notify"
	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
)

var lisbon, _ = time.LoadLocation("Europe/Lisbon")

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, lisbon)
	if err != nil {
		panic(err)
	}
	return t
}

type fakeCalendars struct {
	busy  []schedule.Interval
	stale bool
}

func (f *fakeCalendars) Busy(from, to time.Time) ([]schedule.Interval, error) {
	if f.stale {
		return nil, calsource.ErrStale
	}
	return f.busy, nil
}

func (f *fakeCalendars) RefreshIfOlder(context.Context, time.Duration) error { return nil }

type fakeNotifier struct{ events []notify.Event }

func (f *fakeNotifier) Notify(_ context.Context, ev notify.Event) { f.events = append(f.events, ev) }

type env struct {
	*Service
	cals   *fakeCalendars
	events *fakeNotifier
	now    time.Time
}

// newEnv starts on Sunday 2026-10-25 09:00 with Availability Mon–Fri
// 10:00–12:00, 30-minute steps and a 15-minute buffer.
func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hima.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var weekly [7][]schedule.DayRange
	for d := time.Monday; d <= time.Friday; d++ {
		weekly[d] = []schedule.DayRange{{From: 10 * 60, To: 12 * 60}}
	}
	e := &env{cals: &fakeCalendars{}, events: &fakeNotifier{}, now: at("2026-10-25 09:00")}
	e.Service = &Service{
		Store: st,
		Availability: schedule.Availability{
			Location: lisbon, Weekly: weekly, Buffer: 15 * time.Minute,
			Horizon: 14 * 24 * time.Hour, Step: 30 * time.Minute,
		},
		Calendars: e.cals,
		Now:       func() time.Time { return e.now },
		Notifier:  e.events,
		OwnerName: "Nikolai",
		UIDDomain: "hima.example.com",
	}
	return e
}

func (e *env) invite(t *testing.T, n NewInvite) string {
	t.Helper()
	if n.Durations == nil {
		n.Durations = []time.Duration{30 * time.Minute}
	}
	if n.MaxUses == 0 {
		n.MaxUses = 1
	}
	if n.ExpiresIn == 0 {
		n.ExpiresIn = 7 * 24 * time.Hour
	}
	_, tok, err := e.CreateInvite(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

var alex = GuestDetails{Name: " Alex ", Note: "catch-up"}

func TestBookHappyPath(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{Label: "for Alex", Location: "Café"})

	b, manage, err := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, alex)
	if err != nil {
		t.Fatal(err)
	}
	if b.GuestName != "Alex" || b.Location != "Café" || !b.End.Equal(at("2026-10-26 10:30")) || manage == "" {
		t.Errorf("unexpected booking %+v", b)
	}
	// One use: the Invite is now dead.
	if _, err := e.Invite(ctx, tok); !errors.Is(err, ErrInviteDead) {
		t.Errorf("Invite after last use: err = %v, want ErrInviteDead", err)
	}
}

func TestBookedTimeAndBufferAreNoLongerSlots(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{MaxUses: 5})
	if _, _, err := e.Book(ctx, tok, at("2026-10-26 10:30"), 30*time.Minute, alex); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Invite(ctx, tok)
	slots, err := e.Slots(ctx, inv, 30*time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 10:00 ends inside the buffer before 10:30; 11:00 starts inside the buffer after 11:00.
	if slots[0].Start.Equal(at("2026-10-26 10:00")) || !slots[0].Start.Equal(at("2026-10-26 11:30")) {
		t.Errorf("first slot = %v, want Mon 11:30", slots[0].Start.In(lisbon))
	}
	if _, _, err := e.Book(ctx, tok, at("2026-10-26 10:30"), 30*time.Minute, alex); !errors.Is(err, ErrSlotUnavailable) {
		t.Errorf("double booking: err = %v, want ErrSlotUnavailable", err)
	}
}

func TestCalendarBusyAndStaleness(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{})
	e.cals.busy = []schedule.Interval{{Start: at("2026-10-26 10:00"), End: at("2026-10-26 11:00")}}
	if _, _, err := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, alex); !errors.Is(err, ErrSlotUnavailable) {
		t.Errorf("busy in calendar: err = %v", err)
	}
	e.cals.stale = true
	if _, _, err := e.Book(ctx, tok, at("2026-10-26 11:30"), 30*time.Minute, alex); !errors.Is(err, calsource.ErrStale) {
		t.Errorf("stale calendars: err = %v, want ErrStale", err)
	}
}

func TestBookRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{})
	if _, _, err := e.Book(ctx, tok, at("2026-10-26 10:00"), time.Hour, alex); !errors.Is(err, ErrSlotUnavailable) {
		t.Errorf("duration not on invite: err = %v", err)
	}
	if _, _, err := e.Book(ctx, tok, at("2026-10-26 10:10"), 30*time.Minute, alex); !errors.Is(err, ErrSlotUnavailable) {
		t.Errorf("off-step start: err = %v", err)
	}
	if _, _, err := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, GuestDetails{Name: "  "}); err == nil {
		t.Error("empty name accepted")
	}
	if _, _, err := e.Book(ctx, "nonsense", at("2026-10-26 10:00"), 30*time.Minute, alex); !errors.Is(err, ErrInviteDead) {
		t.Errorf("unknown token: err = %v", err)
	}
}

func TestInviteStatesAreIndistinguishable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	revoked := e.invite(t, NewInvite{})
	inv, _ := e.Invite(ctx, revoked)
	e.Store.RevokeInvite(ctx, inv.ID, e.now)
	expired := e.invite(t, NewInvite{ExpiresIn: time.Hour})
	e.now = e.now.Add(2 * time.Hour)
	for name, tok := range map[string]string{"revoked": revoked, "expired": expired, "unknown": "x"} {
		if _, err := e.Invite(ctx, tok); !errors.Is(err, ErrInviteDead) {
			t.Errorf("%s: err = %v, want ErrInviteDead", name, err)
		}
	}
}

func TestInviteRangeNarrows(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{RangeStart: at("2026-10-28 00:00"), RangeEnd: at("2026-10-29 00:00")})
	inv, _ := e.Invite(ctx, tok)
	slots, _ := e.Slots(ctx, inv, 30*time.Minute, 0)
	for _, s := range slots {
		if s.Start.In(lisbon).Weekday() != time.Wednesday {
			t.Fatalf("slot outside range: %v", s.Start.In(lisbon))
		}
	}
	if len(slots) == 0 {
		t.Fatal("no slots on Wednesday")
	}
}

func TestRescheduleKeepsUseAndOutlivesInvite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{ExpiresIn: time.Hour})
	_, manage, err := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, alex)
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(2 * time.Hour) // Invite expired

	b, err := e.Reschedule(ctx, manage, at("2026-10-27 11:00"))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Start.Equal(at("2026-10-27 11:00")) || b.Sequence != 1 {
		t.Errorf("rescheduled booking %+v", b)
	}
	m, _ := e.Manage(ctx, manage)
	if m.Invite.Uses != 1 || !m.Start.Equal(at("2026-10-27 11:00")) {
		t.Errorf("uses = %d, start = %v", m.Invite.Uses, m.Start)
	}
	// Moving next to its own old time is fine: it does not collide with itself.
	if _, err := e.Reschedule(ctx, manage, at("2026-10-27 10:30")); err != nil {
		t.Errorf("reschedule overlapping own time: %v", err)
	}
}

func TestCancelDoesNotGiveUseBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{})
	_, manage, _ := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, alex)
	if err := e.CancelByGuest(ctx, manage); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Invite(ctx, tok); !errors.Is(err, ErrInviteDead) {
		t.Errorf("invite after cancel: err = %v, want ErrInviteDead", err)
	}
	m, _ := e.Manage(ctx, manage)
	if m.Changeable || m.CancelledBy != store.CancelledByGuest {
		t.Errorf("cancelled booking: %+v", m)
	}
	if err := e.CancelByGuest(ctx, manage); !errors.Is(err, ErrLocked) {
		t.Errorf("second cancel: err = %v", err)
	}
	// The cancelled time is free again for others.
	tok2 := e.invite(t, NewInvite{})
	if _, _, err := e.Book(ctx, tok2, at("2026-10-26 10:00"), 30*time.Minute, alex); err != nil {
		t.Errorf("rebooking cancelled time: %v", err)
	}
}

func TestOwnerCancel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{})
	b, manage, _ := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, alex)
	if err := e.CancelByOwner(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	m, _ := e.Manage(ctx, manage)
	if m.CancelledBy != store.CancelledByOwner {
		t.Errorf("CancelledBy = %q", m.CancelledBy)
	}
}

func TestManageLinkLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{})
	_, manage, _ := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, alex)

	e.now = at("2026-10-26 10:15") // in progress
	if m, err := e.Manage(ctx, manage); err != nil || m.Changeable {
		t.Errorf("started booking should be read-only: %v, %+v", err, m)
	}
	if err := e.CancelByGuest(ctx, manage); !errors.Is(err, ErrLocked) {
		t.Errorf("cancel after start: err = %v", err)
	}
	e.now = at("2026-11-25 10:00") // 30 days after it ended
	if _, err := e.Manage(ctx, manage); err != nil {
		t.Errorf("link should still work just before 30 days: %v", err)
	}
	e.now = at("2026-11-25 10:31")
	if _, err := e.Manage(ctx, manage); !errors.Is(err, ErrNotFound) {
		t.Errorf("link after 30 days: err = %v", err)
	}
}

func TestBusyBlocksOnlyWhenOptedIn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.cals.busy = []schedule.Interval{
		{Start: at("2026-10-26 10:00"), End: at("2026-10-26 11:00")},
		{Start: at("2026-10-29 10:00"), End: at("2026-10-29 11:00")},
	}
	hidden := e.invite(t, NewInvite{})
	shown := e.invite(t, NewInvite{ShowBusy: true, RangeEnd: at("2026-10-27 00:00")})
	inv, _ := e.Invite(ctx, hidden)
	if b, _ := e.BusyBlocks(ctx, inv); len(b) != 0 {
		t.Errorf("hidden invite shows %d busy blocks", len(b))
	}
	inv, _ = e.Invite(ctx, shown)
	if b, _ := e.BusyBlocks(ctx, inv); len(b) != 1 {
		t.Errorf("got %d busy blocks, want 1 (none beyond the invite's range)", len(b))
	}
}

func TestNotifications(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{Label: "for Alex", MaxUses: 2})
	b, manage, _ := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, alex)
	e.Reschedule(ctx, manage, at("2026-10-27 11:00"))
	e.CancelByGuest(ctx, manage)
	b2, _, _ := e.Book(ctx, tok, at("2026-10-28 10:00"), 30*time.Minute, GuestDetails{Name: "Maria"})
	e.CancelByOwner(ctx, b2.ID)
	_ = b

	want := []struct {
		kind    notify.Kind
		title   string
		message string
		silent  bool
	}{
		{notify.Booked, "New booking: Alex", "Mon 26 Oct, 10:00–10:30 · “catch-up” · via for Alex", false},
		{notify.Rescheduled, "Alex rescheduled", "Mon 26 Oct, 10:00–10:30 → Tue 27 Oct, 11:00–11:30", false},
		{notify.CancelledByGuest, "Alex cancelled", "Tue 27 Oct, 11:00–11:30", false},
		{notify.Booked, "New booking: Maria", "Wed 28 Oct, 10:00–10:30 · via for Alex", false},
		{notify.CancelledByOwner, "You cancelled Maria's booking", "Wed 28 Oct, 10:00–10:30", true},
	}
	if len(e.events.events) != len(want) {
		t.Fatalf("got %d events: %+v", len(e.events.events), e.events.events)
	}
	for i, w := range want {
		got := e.events.events[i]
		if got.Kind != w.kind || got.Title != w.title || got.Message != w.message || got.Silent != w.silent || got.Booking == nil {
			t.Errorf("event %d = %+v, want %+v", i, got, w)
		}
	}
}

func TestFailedBookingDoesNotNotify(t *testing.T) {
	e := newEnv(t)
	tok := e.invite(t, NewInvite{})
	e.Book(context.Background(), tok, at("2026-10-26 10:10"), 30*time.Minute, alex)
	if len(e.events.events) != 0 {
		t.Errorf("events: %+v", e.events.events)
	}
}

func TestBookedEventCarriesICS(t *testing.T) {
	e := newEnv(t)
	tok := e.invite(t, NewInvite{Label: "for Alex", Location: "Café"})
	e.Book(context.Background(), tok, at("2026-10-26 10:00"), 30*time.Minute, alex)
	att := e.events.events[0].Attachment
	if att == nil || att.Filename != "booking.ics" {
		t.Fatalf("attachment = %+v", att)
	}
	for _, want := range []string{"UID:booking-1@hima.example.com", "SUMMARY:Alex ↔ Nikolai: catch-up", "LOCATION:Café",
		"DESCRIPTION:catch-up\\n\\nBooked via invite: for Alex", "DTSTART:20261026T100000Z"} {
		if !strings.Contains(string(att.Data), want) {
			t.Errorf("ics missing %q:\n%s", want, att.Data)
		}
	}
}

func TestRescheduleEventCarriesUpdatedICS(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.invite(t, NewInvite{})
	_, manage, _ := e.Book(ctx, tok, at("2026-10-26 10:00"), 30*time.Minute, alex)
	e.Reschedule(ctx, manage, at("2026-10-27 11:00"))
	e.CancelByGuest(ctx, manage)

	att := e.events.events[1].Attachment
	if att == nil {
		t.Fatal("reschedule has no attachment")
	}
	for _, want := range []string{"UID:booking-1@hima.example.com", "SEQUENCE:1", "DTSTART:20261027T110000Z"} {
		if !strings.Contains(string(att.Data), want) {
			t.Errorf("ics missing %q", want)
		}
	}
	if e.events.events[2].Attachment != nil {
		t.Error("cancellation should not carry an attachment")
	}
}
