package store

import (
	"context"
	"database/sql"
	"errors"
)

// ExternalID returns the provider's ID for a written-back Booking, or "".
func (s *Store) ExternalID(ctx context.Context, bookingID int64, provider string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		"SELECT remote_id FROM external_events WHERE booking_id = ? AND provider = ?", bookingID, provider).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Store) SetExternalID(ctx context.Context, bookingID int64, provider, remoteID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO external_events (booking_id, provider, remote_id) VALUES (?, ?, ?)
		ON CONFLICT(booking_id, provider) DO UPDATE SET remote_id = excluded.remote_id`,
		bookingID, provider, remoteID)
	return err
}

func (s *Store) DeleteExternalID(ctx context.Context, bookingID int64, provider string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM external_events WHERE booking_id = ? AND provider = ?", bookingID, provider)
	return err
}

// DeleteSetting removes key.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
	return err
}
