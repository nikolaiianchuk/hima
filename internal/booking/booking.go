// Package booking holds the rules for Invites, Bookings and Manage links
// described in CONTEXT.md, on top of the store, the Owner's Availability and
// their Calendar sources.
package booking

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nikolaiianchuk/hima/internal/notify"
	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/token"
)

var (
	// ErrInviteDead covers unknown, Used up, Expired and Revoked Invites
	// alike, so Guests cannot tell them apart.
	ErrInviteDead = errors.New("invite is no longer valid")
	// ErrSlotUnavailable means the chosen time is not (or no longer) a Slot.
	ErrSlotUnavailable = errors.New("that time is not available")
	// ErrNotFound means a Manage link is unknown or has expired.
	ErrNotFound = errors.New("not found")
	// ErrLocked means the Booking can no longer be changed: it was cancelled
	// or has already started.
	ErrLocked = errors.New("booking can no longer be changed")
)

// InputError is a problem with what a person typed, safe to show them.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// ManageLinkLifetime is how long after a Booking ends its Manage link works.
const ManageLinkLifetime = 30 * 24 * time.Hour

// calendarRefreshAge is how old Calendar source data may be when a Booking
// is confirmed before it is re-fetched.
const calendarRefreshAge = 2 * time.Minute

// Calendars is the Owner's Calendar sources (see calsource.Cache).
type Calendars interface {
	Busy(from, to time.Time) ([]schedule.Interval, error)
	RefreshIfOlder(ctx context.Context, age time.Duration) error
}

type Service struct {
	Store        *store.Store
	Availability schedule.Availability
	Calendars    Calendars
	Now          func() time.Time
	// Notifier, if set, is told about Bookings, reschedules and Cancellations.
	Notifier notify.Notifier
	// OwnerName appears in calendar event titles.
	OwnerName string
	// UIDDomain makes calendar event UIDs globally unique, e.g. the host of
	// base_url.
	UIDDomain string
}

func (s *Service) notify(ctx context.Context, kind notify.Kind, title, message string, b store.Booking) {
	if s.Notifier == nil {
		return
	}
	var attachment *notify.Attachment
	if kind == notify.Booked {
		// Offered to channels that support it; ntfy uses it when attach_ics is on.
		if data, err := s.ownerICS(b); err == nil {
			attachment = &notify.Attachment{Filename: "booking.ics", Data: data}
		}
	}
	s.Notifier.Notify(ctx, notify.Event{
		Attachment: attachment,
		Kind:       kind,
		At:         s.Now(),
		Title:      title,
		Message:    message,
		Booking: &notify.BookingInfo{
			GuestName: b.GuestName, Note: b.Note, Location: b.Location,
			InviteLabel: b.InviteLabel, Start: b.Start, End: b.End,
		},
		Silent: kind == notify.CancelledByOwner,
	})
}

func (s *Service) when(b store.Booking) string {
	return schedule.FormatRange(b.Start, b.End, s.Availability.Location)
}

// NewInvite is what the Owner fills in to create an Invite.
type NewInvite struct {
	Label                string
	Location             string
	Durations            []time.Duration
	MaxUses              int
	ExpiresIn            time.Duration
	RangeStart, RangeEnd time.Time
	ShowBusy             bool
}

// CreateInvite stores an Invite and returns it with its token. The token is
// shown to the Owner once and never stored.
func (s *Service) CreateInvite(ctx context.Context, n NewInvite) (store.Invite, string, error) {
	if len(n.Durations) == 0 {
		return store.Invite{}, "", &InputError{"Pick at least one duration."}
	}
	for _, d := range n.Durations {
		if d <= 0 || d%time.Minute != 0 {
			return store.Invite{}, "", &InputError{fmt.Sprintf("Invalid duration %v.", d)}
		}
	}
	if n.MaxUses < 1 {
		return store.Invite{}, "", &InputError{"An invite needs at least one use."}
	}
	if n.ExpiresIn <= 0 {
		return store.Invite{}, "", &InputError{"An invite must expire in the future."}
	}
	if !n.RangeStart.IsZero() && !n.RangeEnd.IsZero() && !n.RangeStart.Before(n.RangeEnd) {
		return store.Invite{}, "", &InputError{"The date range ends before it starts."}
	}
	now := s.Now()
	inv := store.Invite{
		Label:      strings.TrimSpace(n.Label),
		Location:   strings.TrimSpace(n.Location),
		Durations:  n.Durations,
		MaxUses:    n.MaxUses,
		ExpiresAt:  now.Add(n.ExpiresIn),
		RangeStart: n.RangeStart,
		RangeEnd:   n.RangeEnd,
		ShowBusy:   n.ShowBusy,
		CreatedAt:  now,
	}
	tok := token.New()
	id, err := s.Store.CreateInvite(ctx, inv, token.Hash(tok))
	if err != nil {
		return store.Invite{}, "", err
	}
	inv.ID = id
	return inv, tok, nil
}

// Invite returns the active Invite behind tok, or ErrInviteDead.
func (s *Service) Invite(ctx context.Context, tok string) (store.Invite, error) {
	inv, err := s.Store.InviteByTokenHash(ctx, token.Hash(tok))
	if errors.Is(err, store.ErrNotFound) {
		return inv, ErrInviteDead
	}
	if err != nil {
		return inv, err
	}
	if inv.State(s.Now()) != store.InviteActive {
		return inv, ErrInviteDead
	}
	return inv, nil
}

// Slots lists the Slots of length d within inv's limits. exceptBooking, when
// non-zero, is a Booking being rescheduled whose own time counts as free.
// It returns calsource.ErrStale when Calendar sources are not current.
func (s *Service) Slots(ctx context.Context, inv store.Invite, d time.Duration, exceptBooking int64) ([]schedule.Slot, error) {
	now := s.Now()
	from, to := now, now.Add(s.Availability.Horizon)
	busy, err := s.Calendars.Busy(from, to)
	if err != nil {
		return nil, err
	}
	booked, err := s.Store.BusyBookings(ctx, from, to, exceptBooking)
	if err != nil {
		return nil, err
	}
	return s.Availability.Slots(schedule.Request{
		Now:      now,
		Duration: d,
		From:     inv.RangeStart,
		To:       inv.RangeEnd,
	}, append(busy, booked...)), nil
}

// BusyBlocks returns Busy blocks within inv's date range and the booking
// horizon, for Invites that opt in to showing them.
func (s *Service) BusyBlocks(ctx context.Context, inv store.Invite) ([]schedule.Interval, error) {
	if !inv.ShowBusy {
		return nil, nil
	}
	now := s.Now()
	from, to := now, now.Add(s.Availability.Horizon)
	if inv.RangeStart.After(from) {
		from = inv.RangeStart
	}
	if !inv.RangeEnd.IsZero() && inv.RangeEnd.Before(to) {
		to = inv.RangeEnd
	}
	if !from.Before(to) {
		return nil, nil
	}
	busy, err := s.Calendars.Busy(from, to)
	if err != nil {
		return nil, err
	}
	booked, err := s.Store.BusyBookings(ctx, from, to, 0)
	if err != nil {
		return nil, err
	}
	window := schedule.Interval{Start: from, End: to}
	var clipped []schedule.Interval
	for _, b := range schedule.Merge(append(busy, booked...)) {
		if !b.Overlaps(window) {
			continue
		}
		if b.Start.Before(from) {
			b.Start = from
		}
		if b.End.After(to) {
			b.End = to
		}
		clipped = append(clipped, b)
	}
	return clipped, nil
}

// GuestDetails is what a Guest types when booking.
type GuestDetails struct {
	Name string
	Note string
}

func (g GuestDetails) normalize() (GuestDetails, error) {
	g.Name = strings.TrimSpace(g.Name)
	g.Note = strings.TrimSpace(g.Note)
	switch {
	case g.Name == "":
		return g, &InputError{"Please tell us your name."}
	case utf8.RuneCountInString(g.Name) > 100:
		return g, &InputError{"Your name is too long."}
	case utf8.RuneCountInString(g.Note) > 1000:
		return g, &InputError{"Your note is too long."}
	}
	return g, nil
}

// Book creates a Booking through the Invite behind tok and returns it with
// its Manage link token. Calendar sources are re-checked if stale, so a Slot
// that became busy since the page loaded is refused.
func (s *Service) Book(ctx context.Context, tok string, start time.Time, d time.Duration, g GuestDetails) (store.Booking, string, error) {
	g, err := g.normalize()
	if err != nil {
		return store.Booking{}, "", err
	}
	inv, err := s.Invite(ctx, tok)
	if err != nil {
		return store.Booking{}, "", err
	}
	if !inv.AllowsDuration(d) {
		return store.Booking{}, "", ErrSlotUnavailable
	}
	if err := s.checkSlot(ctx, inv, start, d, 0); err != nil {
		return store.Booking{}, "", err
	}

	b := store.Booking{
		InviteID:    inv.ID,
		InviteLabel: inv.Label,
		GuestName:   g.Name,
		Note:        g.Note,
		Location:    inv.Location,
		Start:       start,
		End:         start.Add(d),
		CreatedAt:   s.Now(),
	}
	manage := token.New()
	b.ID, err = s.Store.CreateBooking(ctx, b, token.Hash(manage), s.Availability.Buffer, s.Now())
	switch {
	case errors.Is(err, store.ErrInviteUnavailable):
		return store.Booking{}, "", ErrInviteDead
	case errors.Is(err, store.ErrSlotTaken):
		return store.Booking{}, "", ErrSlotUnavailable
	case err != nil:
		return store.Booking{}, "", err
	}
	msg := s.when(b)
	if b.Note != "" {
		msg += " · “" + b.Note + "”"
	}
	if b.InviteLabel != "" {
		msg += " · via " + b.InviteLabel
	}
	s.notify(ctx, notify.Booked, "New booking: "+b.GuestName, msg, b)
	return b, manage, nil
}

func (s *Service) checkSlot(ctx context.Context, inv store.Invite, start time.Time, d time.Duration, exceptBooking int64) error {
	// A failed refresh is not fatal here: Busy reports ErrStale if the last
	// good copy has become too old.
	_ = s.Calendars.RefreshIfOlder(ctx, calendarRefreshAge)
	slots, err := s.Slots(ctx, inv, d, exceptBooking)
	if err != nil {
		return err
	}
	for _, sl := range slots {
		if sl.Start.Equal(start) {
			return nil
		}
	}
	return ErrSlotUnavailable
}

// Managed is a Booking reached through its Manage link.
type Managed struct {
	store.Booking
	Invite store.Invite
	// Changeable is false once the Booking is cancelled or has started; the
	// Manage link is then read-only.
	Changeable bool
}

// Manage returns the Booking behind a Manage link token. The link works until
// ManageLinkLifetime after the Booking ends, regardless of its Invite.
func (s *Service) Manage(ctx context.Context, tok string) (Managed, error) {
	b, err := s.Store.BookingByManageHash(ctx, token.Hash(tok))
	if errors.Is(err, store.ErrNotFound) {
		return Managed{}, ErrNotFound
	}
	if err != nil {
		return Managed{}, err
	}
	now := s.Now()
	if now.After(b.End.Add(ManageLinkLifetime)) {
		return Managed{}, ErrNotFound
	}
	inv, err := s.Store.InviteByID(ctx, b.InviteID)
	if err != nil {
		return Managed{}, err
	}
	return Managed{Booking: b, Invite: inv, Changeable: !b.Cancelled() && now.Before(b.Start)}, nil
}

// CancelByGuest cancels through a Manage link. The Invite use is not given
// back.
func (s *Service) CancelByGuest(ctx context.Context, tok string) error {
	m, err := s.Manage(ctx, tok)
	if err != nil {
		return err
	}
	if !m.Changeable {
		return ErrLocked
	}
	if err := s.Store.CancelBooking(ctx, m.ID, store.CancelledByGuest, s.Now()); err != nil {
		return err
	}
	s.notify(ctx, notify.CancelledByGuest, m.GuestName+" cancelled", s.when(m.Booking), m.Booking)
	return nil
}

// RescheduleSlots lists times the Booking behind tok can move to: same
// length, within its Invite's date range, even if the Invite has expired.
func (s *Service) RescheduleSlots(ctx context.Context, m Managed) ([]schedule.Slot, error) {
	return s.Slots(ctx, m.Invite, m.End.Sub(m.Start), m.ID)
}

// Reschedule moves the Booking behind tok to start, keeping its length. It
// does not consume an Invite use.
func (s *Service) Reschedule(ctx context.Context, tok string, start time.Time) (store.Booking, error) {
	m, err := s.Manage(ctx, tok)
	if err != nil {
		return store.Booking{}, err
	}
	if !m.Changeable {
		return store.Booking{}, ErrLocked
	}
	d := m.End.Sub(m.Start)
	if err := s.checkSlot(ctx, m.Invite, start, d, m.ID); err != nil {
		return store.Booking{}, err
	}
	err = s.Store.RescheduleBooking(ctx, m.ID, start, start.Add(d), s.Availability.Buffer)
	if errors.Is(err, store.ErrSlotTaken) {
		return store.Booking{}, ErrSlotUnavailable
	}
	if err != nil {
		return store.Booking{}, err
	}
	b := m.Booking
	b.Start, b.End, b.Sequence = start, start.Add(d), b.Sequence+1
	s.notify(ctx, notify.Rescheduled, b.GuestName+" rescheduled", s.when(m.Booking)+" → "+s.when(b), b)
	return b, nil
}

// CancelByOwner cancels a Booking from the admin.
func (s *Service) CancelByOwner(ctx context.Context, id int64) error {
	b, err := s.Store.BookingByID(ctx, id)
	if err != nil {
		return err
	}
	if b.Cancelled() {
		return ErrLocked
	}
	if err := s.Store.CancelBooking(ctx, id, store.CancelledByOwner, s.Now()); err != nil {
		return err
	}
	s.notify(ctx, notify.CancelledByOwner, "You cancelled "+b.GuestName+"'s booking", s.when(b), b)
	return nil
}
