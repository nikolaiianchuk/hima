package booking

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// CalendarEvent is a Booking as a calendar event. The UID is stable across
// the Guest's download, the Owner feed and ntfy attachments, so calendars
// update rather than duplicate it after a reschedule.
func (s *Service) CalendarEvent(b store.Booking) ics.Event {
	domain := s.UIDDomain
	if domain == "" {
		domain = "hima.invalid"
	}
	summary := b.GuestName + " ↔ " + s.OwnerName
	if b.Note != "" {
		summary += ": " + truncate(b.Note, 60)
	}
	return ics.Event{
		UID:       fmt.Sprintf("booking-%d@%s", b.ID, domain),
		Sequence:  b.Sequence,
		Start:     b.Start,
		End:       b.End,
		Summary:   summary,
		Location:  b.Location,
		Cancelled: b.Cancelled(),
		Stamp:     s.Now(),
	}
}

// OwnerEvent is CalendarEvent with notes for the Owner: the Guest's note and
// the Invite it came through.
func (s *Service) OwnerEvent(b store.Booking) ics.Event {
	ev := s.CalendarEvent(b)
	var parts []string
	if b.Note != "" {
		parts = append(parts, b.Note)
	}
	if b.InviteLabel != "" {
		parts = append(parts, "Booked via invite: "+b.InviteLabel)
	}
	ev.Notes = strings.Join(parts, "\n\n")
	return ev
}

func (s *Service) ownerICS(b store.Booking) ([]byte, error) {
	var buf bytes.Buffer
	if err := ics.Write(&buf, "", s.OwnerEvent(b)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
