package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrNotFound = errors.New("not found")

type InviteState string

const (
	InviteActive  InviteState = "active"
	InviteUsedUp  InviteState = "used up"
	InviteExpired InviteState = "expired"
	InviteRevoked InviteState = "revoked"
)

// Invite is a shareable, expiring link that lets a Guest make Bookings.
// Location kinds.
const (
	LocationNone  = ""
	LocationPlace = "place"
	LocationPhone = "phone"
	LocationVideo = "video"
)

// ValidLocationKind reports whether k is a known Location kind.
func ValidLocationKind(k string) bool {
	switch k {
	case LocationNone, LocationPlace, LocationPhone, LocationVideo:
		return true
	}
	return false
}

type Invite struct {
	ID           int64
	Label        string
	Location     string
	LocationKind string
	Durations    []time.Duration
	MaxUses      int
	ExpiresAt    time.Time
	// RangeStart and RangeEnd narrow Availability; zero means unbounded.
	RangeStart, RangeEnd time.Time
	ShowBusy             bool
	RevokedAt            time.Time // zero unless revoked
	CreatedAt            time.Time
	// Uses counts Bookings made through the Invite, cancelled ones included.
	Uses int
}

func (i Invite) State(now time.Time) InviteState {
	switch {
	case !i.RevokedAt.IsZero():
		return InviteRevoked
	case !now.Before(i.ExpiresAt):
		return InviteExpired
	case i.Uses >= i.MaxUses:
		return InviteUsedUp
	default:
		return InviteActive
	}
}

func (i Invite) AllowsDuration(d time.Duration) bool {
	for _, x := range i.Durations {
		if x == d {
			return true
		}
	}
	return false
}

func (s *Store) CreateInvite(ctx context.Context, inv Invite, tokenHash string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO invites (token_hash, label, location, location_kind, durations, max_uses, expires_at,
		                     range_start, range_end, show_busy, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, inv.Label, inv.Location, inv.LocationKind, formatDurations(inv.Durations), inv.MaxUses,
		inv.ExpiresAt.Unix(), nullTime(inv.RangeStart), nullTime(inv.RangeEnd), inv.ShowBusy,
		inv.CreatedAt.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const inviteColumns = `
	i.id, i.label, i.location, i.location_kind, i.durations, i.max_uses, i.expires_at, i.range_start,
	i.range_end, i.show_busy, i.revoked_at, i.created_at,
	(SELECT count(*) FROM bookings b WHERE b.invite_id = i.id)`

func (s *Store) InviteByTokenHash(ctx context.Context, tokenHash string) (Invite, error) {
	return scanInvite(s.db.QueryRowContext(ctx,
		"SELECT"+inviteColumns+" FROM invites i WHERE i.token_hash = ?", tokenHash))
}

func (s *Store) InviteByID(ctx context.Context, id int64) (Invite, error) {
	return scanInvite(s.db.QueryRowContext(ctx,
		"SELECT"+inviteColumns+" FROM invites i WHERE i.id = ?", id))
}

// ListInvites returns all Invites, newest first.
func (s *Store) ListInvites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT"+inviteColumns+" FROM invites i ORDER BY i.created_at DESC, i.id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvite stops an Invite from creating new Bookings. Existing Bookings
// are untouched.
func (s *Store) RevokeInvite(ctx context.Context, id int64, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE invites SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanInvite(row scanner) (Invite, error) {
	var (
		inv                         Invite
		durations                   string
		expires, created            int64
		rangeStart, rangeEnd, revok sql.NullInt64
	)
	err := row.Scan(&inv.ID, &inv.Label, &inv.Location, &inv.LocationKind, &durations, &inv.MaxUses, &expires,
		&rangeStart, &rangeEnd, &inv.ShowBusy, &revok, &created, &inv.Uses)
	if errors.Is(err, sql.ErrNoRows) {
		return inv, ErrNotFound
	}
	if err != nil {
		return inv, err
	}
	if inv.Durations, err = parseDurations(durations); err != nil {
		return inv, err
	}
	inv.ExpiresAt = time.Unix(expires, 0)
	inv.CreatedAt = time.Unix(created, 0)
	inv.RangeStart = fromNull(rangeStart)
	inv.RangeEnd = fromNull(rangeEnd)
	inv.RevokedAt = fromNull(revok)
	return inv, nil
}

func formatDurations(ds []time.Duration) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = strconv.Itoa(int(d / time.Minute))
	}
	return strings.Join(parts, ",")
}

func parseDurations(s string) ([]time.Duration, error) {
	var out []time.Duration
	for _, p := range strings.Split(s, ",") {
		m, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invite durations %q: %w", s, err)
		}
		out = append(out, time.Duration(m)*time.Minute)
	}
	return out, nil
}

func nullTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

func fromNull(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(n.Int64, 0)
}

// UpdateInvite changes what may change after an Invite is shared: its Label,
// use count and expiry. Revoked Invites cannot be changed.
func (s *Store) UpdateInvite(ctx context.Context, id int64, label string, maxUses int, expiresAt time.Time) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE invites SET label = ?, max_uses = ?, expires_at = ? WHERE id = ? AND revoked_at IS NULL",
		label, maxUses, expiresAt.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
