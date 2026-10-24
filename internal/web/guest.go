package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/nikolaiianchuk/hima/internal/booking"
	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

const slotTakenMsg = "Sorry, that time was just taken. Please pick another."

// pickDuration returns the duration named by ?d= (minutes) if the Invite
// allows it, otherwise the Invite's first duration.
func pickDuration(inv store.Invite, raw string) time.Duration {
	if m, err := strconv.Atoi(raw); err == nil {
		if d := time.Duration(m) * time.Minute; inv.AllowsDuration(d) {
			return d
		}
	}
	return inv.Durations[0]
}

func parseStart(raw string) (time.Time, bool) {
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

func hasSlot(slots []schedule.Slot, start time.Time) bool {
	for _, s := range slots {
		if s.Start.Equal(start) {
			return true
		}
	}
	return false
}

// slotsPage builds the Slot picker. path is the page's own path; keep holds
// query parameters (besides tz and start) every link must carry.
func (s *Server) slotsPage(path string, keep url.Values, slots []schedule.Slot, busy []schedule.Interval, loc *time.Location) views.SlotsPage {
	days := map[string]*views.SlotDay{}
	var order []time.Time
	day := func(t time.Time) *views.SlotDay {
		local := t.In(loc)
		key := local.Format("2006-01-02")
		if d, ok := days[key]; ok {
			return d
		}
		d := &views.SlotDay{Label: dayLabel(local)}
		days[key] = d
		order = append(order, time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc))
		return d
	}
	for _, sl := range slots {
		q := cloneValues(keep)
		q.Set("tz", loc.String())
		q.Set("start", strconv.FormatInt(sl.Start.Unix(), 10))
		d := day(sl.Start)
		d.Slots = append(d.Slots, views.SlotLink{Time: clockLabel(sl.Start.In(loc)), Href: withQuery(path, q)})
	}
	for _, b := range busy {
		d := day(b.Start)
		d.Busy = append(d.Busy, timeRangeLabel(b.Start, b.End, loc))
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Before(order[j]) })
	page := views.SlotsPage{TZ: views.TZForm{Current: loc.String(), Choices: zoneChoices(loc.String()), Keep: map[string]string{}}}
	for k := range keep {
		page.TZ.Keep[k] = keep.Get(k)
	}
	for _, t := range order {
		page.Days = append(page.Days, *days[t.Format("2006-01-02")])
	}
	return page
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	inv, err := s.svc.Invite(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	loc := s.guestZone(q)
	d := pickDuration(inv, q.Get("d"))
	start, picked := parseStart(q.Get("start"))
	s.renderInvite(w, r, tok, inv, d, loc, start, picked, http.StatusOK, "")
}

// renderInvite shows the confirm form when a valid start is picked, and the
// Slot picker otherwise.
func (s *Server) renderInvite(w http.ResponseWriter, r *http.Request, tok string, inv store.Invite, d time.Duration, loc *time.Location, start time.Time, picked bool, status int, errMsg string) {
	path := "/i/" + tok
	slots, err := s.svc.Slots(r.Context(), inv, d, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	keep := url.Values{"d": {minutes(d)}}
	if picked {
		if hasSlot(slots, start) {
			s.render(w, r, status, views.Confirm(s.confirmPage(path, keep, start, d, loc, inv.Location, errMsg, r.PostFormValue("name"), r.PostFormValue("note"))))
			return
		}
		errMsg = slotTakenMsg
		status = http.StatusConflict
	}
	busy, err := s.svc.BusyBlocks(r.Context(), inv)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page := s.slotsPage(path, keep, slots, busy, loc)
	page.Title = "Book time with " + s.cfg.Owner.Name
	page.Intro = "Pick a time that suits you."
	page.Location = inv.Location
	page.Error = errMsg
	if len(inv.Durations) > 1 {
		for _, opt := range inv.Durations {
			page.Durations = append(page.Durations, views.Choice{
				Label:  durationLabel(opt),
				Href:   withQuery(path, url.Values{"d": {minutes(opt)}, "tz": {loc.String()}}),
				Active: opt == d,
			})
		}
	}
	s.render(w, r, status, views.Slots(page))
}

func (s *Server) confirmPage(path string, keep url.Values, start time.Time, d time.Duration, loc *time.Location, location, errMsg, name, note string) views.ConfirmPage {
	back := cloneValues(keep)
	back.Set("tz", loc.String())
	return views.ConfirmPage{
		Title:      "Confirm your time",
		When:       whenLabel(start, start.Add(d), loc) + " (" + loc.String() + ")",
		OwnerWhen:  s.ownerWhen(start, start.Add(d), loc),
		Duration:   durationLabel(d),
		Location:   location,
		Action:     path,
		Start:      strconv.FormatInt(start.Unix(), 10),
		Minutes:    minutes(d),
		TZ:         loc.String(),
		AskDetails: true,
		Name:       name,
		Note:       note,
		Error:      errMsg,
		BackHref:   withQuery(path, back),
		Submit:     "Book it",
	}
}

func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	inv, err := s.svc.Invite(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	loc := s.guestZone(url.Values{"tz": {r.PostFormValue("tz")}})
	d := pickDuration(inv, r.PostFormValue("d"))
	start, ok := parseStart(r.PostFormValue("start"))
	if !ok {
		s.renderInvite(w, r, tok, inv, d, loc, time.Time{}, false, http.StatusBadRequest, "Please pick a time.")
		return
	}
	_, manage, err := s.svc.Book(r.Context(), tok, start, d, booking.GuestDetails{
		Name: r.PostFormValue("name"),
		Note: r.PostFormValue("note"),
	})
	var inputErr *booking.InputError
	switch {
	case errors.As(err, &inputErr):
		s.renderInvite(w, r, tok, inv, d, loc, start, true, http.StatusUnprocessableEntity, inputErr.Msg)
		return
	case errors.Is(err, booking.ErrSlotUnavailable):
		s.renderInvite(w, r, tok, inv, d, loc, time.Time{}, false, http.StatusConflict, slotTakenMsg)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, withQuery("/b/"+manage, url.Values{"new": {"1"}, "tz": {loc.String()}}), http.StatusSeeOther)
}

func (s *Server) handleManage(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	m, err := s.svc.Manage(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	loc := s.guestZone(q)
	tzq := url.Values{"tz": {loc.String()}}
	base := "/b/" + tok
	s.render(w, r, http.StatusOK, views.Manage(views.ManagePage{
		JustBooked:     q.Get("new") == "1",
		JustChanged:    q.Get("changed") == "1",
		Title:          m.GuestName + " ↔ " + s.cfg.Owner.Name,
		When:           whenLabel(m.Start, m.End, loc) + " (" + loc.String() + ")",
		OwnerWhen:      s.ownerWhen(m.Start, m.End, loc),
		Duration:       durationLabel(m.End.Sub(m.Start)),
		Location:       m.Location,
		Note:           m.Note,
		Status:         s.guestStatus(m),
		Cancelled:      m.Cancelled(),
		Changeable:     m.Changeable,
		ManageURL:      s.baseURL(r) + base,
		ICSHref:        base + "/event.ics",
		CancelAction:   withQuery(base+"/cancel", tzq),
		RescheduleHref: withQuery(base+"/reschedule", tzq),
		TZ:             loc.String(),
	}))
}

func (s *Server) guestStatus(m booking.Managed) string {
	now := s.svc.Now()
	switch {
	case m.CancelledBy == store.CancelledByGuest:
		return "Cancelled by you."
	case m.CancelledBy == store.CancelledByOwner:
		return "Cancelled by " + s.cfg.Owner.Name + "."
	case !now.Before(m.End):
		return "This meeting has taken place."
	case !now.Before(m.Start):
		return "Happening now."
	default:
		return "Confirmed with " + s.cfg.Owner.Name + "."
	}
}

func (s *Server) handleBookingICS(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	m, err := s.svc.Manage(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ev := s.svc.CalendarEvent(m.Booking)
	ev.URL = s.baseURL(r) + "/b/" + tok
	ev.Notes = joinNonEmpty(m.Note, "Reschedule or cancel: "+ev.URL)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hima-booking.ics"`)
	if err := ics.Write(w, "", ev); err != nil {
		s.log.Error("write ics", "err", err)
	}
}

func (s *Server) handleGuestCancel(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	err := s.svc.CancelByGuest(r.Context(), tok)
	q := url.Values{"tz": {s.guestZone(r.URL.Query()).String()}}
	switch {
	case errors.Is(err, booking.ErrLocked):
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		q.Set("changed", "1")
	}
	http.Redirect(w, r, withQuery("/b/"+tok, q), http.StatusSeeOther)
}

func (s *Server) handleRescheduleForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start, picked := parseStart(q.Get("start"))
	s.renderReschedule(w, r, s.guestZone(q), start, picked, http.StatusOK, "")
}

func (s *Server) renderReschedule(w http.ResponseWriter, r *http.Request, loc *time.Location, start time.Time, picked bool, status int, errMsg string) {
	tok := r.PathValue("token")
	m, err := s.svc.Manage(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	manage := withQuery("/b/"+tok, url.Values{"tz": {loc.String()}})
	if !m.Changeable {
		http.Redirect(w, r, manage, http.StatusSeeOther)
		return
	}
	slots, err := s.svc.RescheduleSlots(r.Context(), m)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	path := "/b/" + tok + "/reschedule"
	d := m.End.Sub(m.Start)
	if picked {
		if hasSlot(slots, start) {
			p := s.confirmPage(path, url.Values{}, start, d, loc, m.Location, errMsg, "", "")
			p.Title, p.AskDetails, p.Submit = "Move your booking here?", false, "Move it"
			s.render(w, r, status, views.Confirm(p))
			return
		}
		errMsg, status = slotTakenMsg, http.StatusConflict
	}
	page := s.slotsPage(path, url.Values{}, slots, nil, loc)
	page.Title = "Pick a new time"
	page.Intro = "Currently " + whenLabel(m.Start, m.End, loc) + "."
	page.Location = m.Location
	page.Error = errMsg
	s.render(w, r, status, views.Slots(page))
}

func (s *Server) handleReschedule(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	loc := s.guestZone(url.Values{"tz": {r.PostFormValue("tz")}})
	start, ok := parseStart(r.PostFormValue("start"))
	if !ok {
		s.renderReschedule(w, r, loc, time.Time{}, false, http.StatusBadRequest, "Please pick a time.")
		return
	}
	_, err := s.svc.Reschedule(r.Context(), tok, start)
	switch {
	case errors.Is(err, booking.ErrSlotUnavailable):
		s.renderReschedule(w, r, loc, time.Time{}, false, http.StatusConflict, slotTakenMsg)
		return
	case errors.Is(err, booking.ErrLocked):
		http.Redirect(w, r, withQuery("/b/"+tok, url.Values{"tz": {loc.String()}}), http.StatusSeeOther)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, withQuery("/b/"+tok, url.Values{"changed": {"1"}, "tz": {loc.String()}}), http.StatusSeeOther)
}

func joinNonEmpty(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += p
	}
	return out
}
