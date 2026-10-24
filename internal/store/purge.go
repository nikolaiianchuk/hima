package store

import (
	"context"
	"time"
)

// Purged counts what Purge deleted.
type Purged struct {
	Bookings, Invites, Activity, Sessions int64
}

// Purge deletes Bookings that ended before cutoff, Invites that stopped
// being usable before cutoff and have no Bookings left, activity older than
// cutoff, and sessions expired by now.
func (s *Store) Purge(ctx context.Context, cutoff, now time.Time) (Purged, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Purged{}, err
	}
	defer tx.Rollback()

	var p Purged
	steps := []struct {
		n     *int64
		query string
		args  []any
	}{
		{&p.Bookings, "DELETE FROM bookings WHERE ends_at < ?", []any{cutoff.Unix()}},
		{&p.Invites, `
			DELETE FROM invites
			WHERE (expires_at < ? OR revoked_at < ?)
			  AND NOT EXISTS (SELECT 1 FROM bookings b WHERE b.invite_id = invites.id)`,
			[]any{cutoff.Unix(), cutoff.Unix()}},
		{&p.Activity, "DELETE FROM activity WHERE at < ?", []any{cutoff.Unix()}},
		{&p.Sessions, "DELETE FROM sessions WHERE expires_at <= ?", []any{now.Unix()}},
	}
	for _, st := range steps {
		res, err := tx.ExecContext(ctx, st.query, st.args...)
		if err != nil {
			return Purged{}, err
		}
		*st.n, _ = res.RowsAffected()
	}
	return p, tx.Commit()
}
