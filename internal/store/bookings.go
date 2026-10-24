package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nikolaiianchuk/hima/internal/schedule"
)

var (
	// ErrInviteUnavailable means the Invite can no longer create Bookings.
	ErrInviteUnavailable = errors.New("invite is no longer active")
	// ErrSlotTaken means another Booking (plus buffer) overlaps the time.
	ErrSlotTaken = errors.New("slot is already taken")
	// ErrDayFull means the day already has its maximum of Bookings.
	ErrDayFull = errors.New("day is fully booked")
)

// DayCap limits live Bookings starting in [Start, End) to Max; a zero Max
// means no limit.
type DayCap struct {
	Start, End time.Time
	Max        int
}

const (
	CancelledByGuest = "guest"
	CancelledByOwner = "owner"
)

// Booking is a reserved time with the Owner, made by a Guest through an Invite.
type Booking struct {
	ID           int64
	InviteID     int64
	InviteLabel  string
	GuestName    string
	Note         string
	Location     string
	LocationKind string
	Start, End   time.Time
	Sequence     int
	// Reschedules counts moves by the Guest.
	Reschedules int
	CancelledAt time.Time // zero unless cancelled
	CancelledBy string
	CreatedAt   time.Time
}

func (b Booking) Cancelled() bool { return !b.CancelledAt.IsZero() }

// CreateBooking inserts b if its Invite is still active at now and no other
// live Booking is within buffer of it. Both checks run in the same
// transaction as the insert, so concurrent Guests cannot double-book.
func (s *Store) CreateBooking(ctx context.Context, b Booking, manageTokenHash string, buffer time.Duration, dayCap DayCap, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	inv, err := scanInvite(tx.QueryRowContext(ctx, "SELECT"+inviteColumns+" FROM invites i WHERE i.id = ?", b.InviteID))
	if err != nil {
		return 0, err
	}
	if inv.State(now) != InviteActive {
		return 0, ErrInviteUnavailable
	}
	if err := checkFree(ctx, tx, b.Start, b.End, buffer, 0); err != nil {
		return 0, err
	}
	if err := checkDayCap(ctx, tx, dayCap, 0); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO bookings (invite_id, manage_token_hash, guest_name, note, location, location_kind, starts_at, ends_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.InviteID, manageTokenHash, b.GuestName, b.Note, b.Location, b.LocationKind, b.Start.Unix(), b.End.Unix(), b.CreatedAt.Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// RescheduleBooking moves a live Booking, checking for overlaps as
// CreateBooking does. It does not consume an Invite use.
func (s *Store) RescheduleBooking(ctx context.Context, id int64, start, end time.Time, buffer time.Duration, dayCap DayCap) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkFree(ctx, tx, start, end, buffer, id); err != nil {
		return err
	}
	if err := checkDayCap(ctx, tx, dayCap, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE bookings SET starts_at = ?, ends_at = ?, sequence = sequence + 1, reschedules = reschedules + 1
		WHERE id = ? AND cancelled_at IS NULL`, start.Unix(), end.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func checkFree(ctx context.Context, tx *sql.Tx, start, end time.Time, buffer time.Duration, exceptID int64) error {
	var n int
	err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM bookings
		WHERE cancelled_at IS NULL AND id != ? AND starts_at < ? AND ends_at > ?`,
		exceptID, end.Add(buffer).Unix(), start.Add(-buffer).Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrSlotTaken
	}
	return nil
}

func checkDayCap(ctx context.Context, tx *sql.Tx, c DayCap, exceptID int64) error {
	if c.Max <= 0 {
		return nil
	}
	var n int
	err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM bookings
		WHERE cancelled_at IS NULL AND id != ? AND starts_at >= ? AND starts_at < ?`,
		exceptID, c.Start.Unix(), c.End.Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n >= c.Max {
		return ErrDayFull
	}
	return nil
}

// CancelBooking records a Cancellation. Cancelling twice is an ErrNotFound.
func (s *Store) CancelBooking(ctx context.Context, id int64, by string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE bookings SET cancelled_at = ?, cancelled_by = ? WHERE id = ? AND cancelled_at IS NULL",
		now.Unix(), by, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const bookingColumns = `
	b.id, b.invite_id, i.label, b.guest_name, b.note, b.location, b.location_kind, b.starts_at, b.ends_at,
	b.sequence, b.reschedules, b.cancelled_at, coalesce(b.cancelled_by, ''), b.created_at`

func (s *Store) BookingByManageHash(ctx context.Context, hash string) (Booking, error) {
	return scanBooking(s.db.QueryRowContext(ctx,
		"SELECT"+bookingColumns+" FROM bookings b JOIN invites i ON i.id = b.invite_id WHERE b.manage_token_hash = ?", hash))
}

func (s *Store) BookingByID(ctx context.Context, id int64) (Booking, error) {
	return scanBooking(s.db.QueryRowContext(ctx,
		"SELECT"+bookingColumns+" FROM bookings b JOIN invites i ON i.id = b.invite_id WHERE b.id = ?", id))
}

// ListBookings returns Bookings ending after since, soonest first.
func (s *Store) ListBookings(ctx context.Context, since time.Time) ([]Booking, error) {
	return s.queryBookings(ctx,
		"SELECT"+bookingColumns+" FROM bookings b JOIN invites i ON i.id = b.invite_id WHERE b.ends_at > ? ORDER BY b.starts_at",
		since.Unix())
}

// BookingsForInvite returns an Invite's Bookings, soonest first.
func (s *Store) BookingsForInvite(ctx context.Context, inviteID int64) ([]Booking, error) {
	return s.queryBookings(ctx,
		"SELECT"+bookingColumns+" FROM bookings b JOIN invites i ON i.id = b.invite_id WHERE b.invite_id = ? ORDER BY b.starts_at",
		inviteID)
}

func (s *Store) queryBookings(ctx context.Context, query string, args ...any) ([]Booking, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Booking
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BusyBookings returns live Bookings overlapping [from, to) as Busy blocks,
// leaving out exceptID (a Booking being rescheduled).
func (s *Store) BusyBookings(ctx context.Context, from, to time.Time, exceptID int64) ([]schedule.Interval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT starts_at, ends_at FROM bookings
		WHERE cancelled_at IS NULL AND id != ? AND starts_at < ? AND ends_at > ?`,
		exceptID, to.Unix(), from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schedule.Interval
	for rows.Next() {
		var start, end int64
		if err := rows.Scan(&start, &end); err != nil {
			return nil, err
		}
		out = append(out, schedule.Interval{Start: time.Unix(start, 0), End: time.Unix(end, 0)})
	}
	return out, rows.Err()
}

func scanBooking(row scanner) (Booking, error) {
	var (
		b                   Booking
		start, end, created int64
		cancelled           sql.NullInt64
	)
	err := row.Scan(&b.ID, &b.InviteID, &b.InviteLabel, &b.GuestName, &b.Note, &b.Location, &b.LocationKind,
		&start, &end, &b.Sequence, &b.Reschedules, &cancelled, &b.CancelledBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	b.Start, b.End, b.CreatedAt = time.Unix(start, 0), time.Unix(end, 0), time.Unix(created, 0)
	b.CancelledAt = fromNull(cancelled)
	return b, nil
}

// UpdateBookingDetails changes a live Booking's Location and note, bumping
// its sequence so calendars pick up the change.
func (s *Store) UpdateBookingDetails(ctx context.Context, id int64, location, note string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE bookings SET location = ?, note = ?, sequence = sequence + 1
		WHERE id = ? AND cancelled_at IS NULL`, location, note, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
