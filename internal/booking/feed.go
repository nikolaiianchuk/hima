package booking

import (
	"context"
	"crypto/subtle"
	"strconv"
	"time"

	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/token"
)

// FeedHistory is how far back the Owner feed lists Bookings.
const FeedHistory = 30 * 24 * time.Hour

// RotateFeed creates a new Owner feed token, invalidating the previous one.
// Like Invite tokens, it is shown once and only its hash is kept.
func (s *Service) RotateFeed(ctx context.Context) (string, error) {
	tok := token.New()
	if err := s.Store.SetSetting(ctx, store.SettingFeedTokenHash, token.Hash(tok)); err != nil {
		return "", err
	}
	if err := s.Store.SetSetting(ctx, store.SettingFeedCreatedAt, strconv.FormatInt(s.Now().Unix(), 10)); err != nil {
		return "", err
	}
	return tok, nil
}

// FeedCreatedAt returns when the current Owner feed link was made, or zero if
// there is none.
func (s *Service) FeedCreatedAt(ctx context.Context) (time.Time, error) {
	v, err := s.Store.Setting(ctx, store.SettingFeedCreatedAt)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0), nil
}

// FeedBookings returns the Bookings for the Owner feed if tok is the current
// feed token, including cancelled ones so calendars remove them.
func (s *Service) FeedBookings(ctx context.Context, tok string) ([]store.Booking, error) {
	want, err := s.Store.Setting(ctx, store.SettingFeedTokenHash)
	if err != nil {
		return nil, err
	}
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(token.Hash(tok))) != 1 {
		return nil, ErrNotFound
	}
	return s.Store.ListBookings(ctx, s.Now().Add(-FeedHistory))
}
