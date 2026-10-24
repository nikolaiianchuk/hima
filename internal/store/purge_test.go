package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPurge(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hima.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -90)
	old, recent := cutoff.Add(-24*time.Hour), cutoff.Add(24*time.Hour)

	invite := func(expires time.Time) int64 {
		id, err := s.CreateInvite(ctx, Invite{Durations: []time.Duration{30 * time.Minute}, MaxUses: 5, ExpiresAt: expires, CreatedAt: old.AddDate(0, 0, -7)}, expires.String())
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// book makes a Booking at bookedAt (while its Invite is active) for a
	// meeting ending at end.
	book := func(inviteID int64, bookedAt, end time.Time, hash string) {
		if _, err := s.CreateBooking(ctx, Booking{InviteID: inviteID, GuestName: "g", Start: end.Add(-time.Hour), End: end, CreatedAt: bookedAt},
			hash, 0, bookedAt); err != nil {
			t.Fatal(err)
		}
	}

	oldInvite := invite(old)                         // expired long ago, no bookings left after purge
	keptByBooking := invite(old.Add(-time.Hour * 2)) // expired long ago but has a recent booking
	activeInvite := invite(now.AddDate(0, 0, 7))
	book(oldInvite, old.AddDate(0, 0, -7), old.Add(-3*time.Hour), "b1")
	book(keptByBooking, old.AddDate(0, 0, -7), recent, "b2")
	_ = activeInvite

	s.AddActivity(ctx, Activity{At: old, Kind: "booked", Title: "old"})
	s.AddActivity(ctx, Activity{At: recent, Kind: "booked", Title: "recent"})
	s.CreateSession(ctx, "expired", now.Add(-time.Minute))
	s.CreateSession(ctx, "valid", now.Add(time.Hour))

	p, err := s.Purge(ctx, cutoff, now)
	if err != nil {
		t.Fatal(err)
	}
	if p != (Purged{Bookings: 1, Invites: 1, Activity: 1, Sessions: 1}) {
		t.Errorf("purged %+v", p)
	}
	if _, err := s.InviteByID(ctx, oldInvite); err != ErrNotFound {
		t.Errorf("old invite still there: %v", err)
	}
	for _, id := range []int64{keptByBooking, activeInvite} {
		if _, err := s.InviteByID(ctx, id); err != nil {
			t.Errorf("invite %d purged: %v", id, err)
		}
	}
	if ok, _ := s.SessionValid(ctx, "valid", now); !ok {
		t.Error("valid session purged")
	}
}
