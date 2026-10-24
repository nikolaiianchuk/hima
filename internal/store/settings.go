package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Setting keys.
const (
	SettingFeedTokenHash = "feed_token_hash"
	SettingFeedCreatedAt = "feed_created_at"
)

// Setting returns the value for key, or "" if unset.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value)
	return err
}

// Activity is one entry in the admin's activity log.
type Activity struct {
	At      time.Time
	Kind    string
	Title   string
	Message string
}

func (s *Store) AddActivity(ctx context.Context, a Activity) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO activity (at, kind, title, message) VALUES (?, ?, ?, ?)",
		a.At.Unix(), a.Kind, a.Title, a.Message)
	return err
}

// RecentActivity returns the latest entries, newest first.
func (s *Store) RecentActivity(ctx context.Context, limit int) ([]Activity, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT at, kind, title, message FROM activity ORDER BY at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var (
			a  Activity
			at int64
		)
		if err := rows.Scan(&at, &a.Kind, &a.Title, &a.Message); err != nil {
			return nil, err
		}
		a.At = time.Unix(at, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}
