package web

import (
	"net/http"
	"strings"

	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

// handleFeed serves the Owner feed. The token may carry an .ics suffix, which
// some calendar apps expect.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSuffix(r.PathValue("token"), ".ics")
	bookings, err := s.svc.FeedBookings(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	events := make([]ics.Event, len(bookings))
	for i, b := range bookings {
		ev := s.bookingEvent(r, b)
		var via string
		if b.InviteLabel != "" {
			via = "Booked via invite: " + b.InviteLabel
		}
		ev.Notes = joinNonEmpty(b.Note, via)
		events[i] = ev
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	if err := ics.Write(w, "hima: bookings with "+s.cfg.Owner.Name, events...); err != nil {
		s.log.Error("write feed", "err", err)
	}
}

func (s *Server) handleRotateFeed(w http.ResponseWriter, r *http.Request) {
	tok, err := s.svc.RotateFeed(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, views.FeedCreated(s.baseURL(r)+"/feed/"+tok+".ics"))
}
