package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// guestWindow is when an Invite can offer Slots: Availability's notice and
// horizon, narrowed by the Invite's date range.
func (s *Server) guestWindow(inv store.Invite) (time.Time, time.Time) {
	now, a := s.svc.Now(), s.svc.Availability
	from, to := now.Add(a.MinNotice), now.Add(a.Horizon)
	if inv.RangeStart.After(from) {
		from = inv.RangeStart
	}
	if !inv.RangeEnd.IsZero() && inv.RangeEnd.Before(to) {
		to = inv.RangeEnd
	}
	return from, to
}

func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// monday returns the Monday on or before the midnight m.
func monday(m time.Time) time.Time {
	return m.AddDate(0, 0, -((int(m.Weekday()) + 6) % 7))
}

func dateKey(t time.Time) string { return t.Format("2006-01-02") }

func dateRangeLabel(from, to time.Time, loc *time.Location) string {
	if !from.Before(to) {
		return ""
	}
	return dateShort(from.In(loc)) + " – " + dateShort(to.Add(-time.Nanosecond).In(loc))
}

// picker describes one Slot picker page.
type picker struct {
	path string         // the page's own path
	keep url.Values     // parameters every link carries (besides tz, from, day)
	loc  *time.Location // the Guest's zone
	q    url.Values     // the request's query
}

// slotPicker lays out two weeks of days and the selected day's Slots.
// ?from= picks the fortnight (any date in its first week), ?day= the day.
func (s *Server) slotPicker(p picker, slots []schedule.Slot, winFrom, winTo time.Time, busy []schedule.Interval, showBusy bool) views.SlotsPage {
	loc := p.loc
	byDay := map[string][]schedule.Slot{}
	for _, sl := range slots {
		k := dateKey(sl.Start.In(loc))
		byDay[k] = append(byDay[k], sl)
	}

	// Fortnights start on the Monday of the first week with a free time, so
	// the grid never opens on days that have already gone.
	minPage := monday(midnight(winFrom, loc))
	if len(slots) > 0 {
		minPage = monday(midnight(slots[0].Start, loc))
	}
	last := midnight(winTo.Add(-time.Nanosecond), loc)
	page := minPage
	if f, err := time.ParseInLocation("2006-01-02", p.q.Get("from"), loc); err == nil && f.After(minPage) {
		fortnights := calendarDays(minPage, f, loc) / 14
		page = minPage.AddDate(0, 0, 14*fortnights)
	}

	var cells []time.Time
	for i := 0; i < 14; i++ {
		cells = append(cells, page.AddDate(0, 0, i))
	}
	selected := ""
	if d := p.q.Get("day"); d != "" && len(byDay[d]) > 0 {
		for _, c := range cells {
			if dateKey(c) == d {
				selected = d
			}
		}
	}
	for _, c := range cells {
		if selected == "" && len(byDay[dateKey(c)]) > 0 {
			selected = dateKey(c)
		}
	}

	base := cloneValues(p.keep)
	base.Set("tz", loc.String())
	base.Set("from", dateKey(page))
	out := views.SlotsPage{TZ: views.TZPicker{Current: loc.String(), Choices: zoneChoices(loc.String()), Keep: map[string]string{}}}
	for k := range p.keep {
		out.TZ.Keep[k] = p.keep.Get(k)
	}
	out.TZ.Keep["from"] = dateKey(page)
	if selected != "" {
		out.TZ.Keep["day"] = selected
	}

	for _, c := range cells {
		k := dateKey(c)
		open := len(byDay[k]) > 0
		q := cloneValues(base)
		q.Set("day", k)
		label := c.Format("Mon 2 Jan")
		if !open {
			label += ", no times"
		}
		out.Days = append(out.Days, views.DayCell{N: strconv.Itoa(c.Day()), Label: label, Href: withQuery(p.path, q), Open: open, Selected: k == selected})
	}
	if page.After(minPage) {
		q := cloneValues(base)
		q.Set("from", dateKey(page.AddDate(0, 0, -14)))
		out.PrevHref = withQuery(p.path, q)
	}
	if next := page.AddDate(0, 0, 14); !next.After(last) {
		q := cloneValues(base)
		q.Set("from", dateKey(next))
		out.NextHref = withQuery(p.path, q)
	}

	if selected == "" {
		out.DayHeading = "No free times"
		out.CountLabel = "in these two weeks"
		return out
	}
	day, _ := time.ParseInLocation("2006-01-02", selected, loc)
	out.DayHeading = dayHeading(day)
	n := len(byDay[selected])
	out.CountLabel = fmt.Sprintf("%d times", n)
	if n == 1 {
		out.CountLabel = "1 time"
	}
	for _, sl := range byDay[selected] {
		q := cloneValues(base)
		q.Set("day", selected)
		q.Set("start", strconv.FormatInt(sl.Start.Unix(), 10))
		out.Slots = append(out.Slots, views.SlotLink{Time: clockLabel(sl.Start.In(loc)), Href: withQuery(p.path, q)})
	}
	if showBusy {
		out.Busy = s.busyBar(day, loc, busy)
	}
	return out
}

// busyBar shows a Guest-local day's busy and free time in 30-minute
// segments, spanning the Owner's Availability that falls on that day.
func (s *Server) busyBar(day time.Time, loc *time.Location, busy []schedule.Interval) *views.BusyBar {
	a := s.svc.Availability
	dayEnd := day.AddDate(0, 0, 1)
	var lo, hi time.Time
	ownerDay := midnight(day, a.Location)
	for offset := -1; offset <= 1; offset++ {
		od := ownerDay.AddDate(0, 0, offset)
		for _, r := range a.Weekly[od.Weekday()] {
			st := time.Date(od.Year(), od.Month(), od.Day(), int(r.From)/60, int(r.From)%60, 0, 0, a.Location)
			en := time.Date(od.Year(), od.Month(), od.Day(), int(r.To)/60, int(r.To)%60, 0, 0, a.Location)
			if st.Before(day) {
				st = day
			}
			if en.After(dayEnd) {
				en = dayEnd
			}
			if !st.Before(en) {
				continue
			}
			if lo.IsZero() || st.Before(lo) {
				lo = st
			}
			if en.After(hi) {
				hi = en
			}
		}
	}
	if lo.IsZero() {
		return nil
	}
	l := lo.In(loc)
	lo = time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, loc)
	if h := hi.In(loc); h.Minute() != 0 || h.Second() != 0 {
		hi = time.Date(h.Year(), h.Month(), h.Day(), h.Hour(), 0, 0, 0, loc).Add(time.Hour)
	}
	bar := &views.BusyBar{Aria: "Busy and free time, " + clockLabel(lo.In(loc)) + " to " + clockLabel(hi.In(loc))}
	for t := lo; t.Before(hi); t = t.Add(30 * time.Minute) {
		seg := schedule.Interval{Start: t, End: t.Add(30 * time.Minute)}
		taken := false
		for _, b := range busy {
			if b.Overlaps(seg) {
				taken = true
				break
			}
		}
		bar.Busy = append(bar.Busy, taken)
	}
	hours := int(hi.Sub(lo).Hours())
	step := 2
	for hours/step > 6 {
		step++
	}
	for h := 0; h <= hours; h += step {
		bar.Labels = append(bar.Labels, lo.Add(time.Duration(h)*time.Hour).In(loc).Format("15"))
	}
	return bar
}

// locationBeforeBooking is the Location line on the confirm page: calls
// reveal their details only after booking.
func locationBeforeBooking(kind, value string) string {
	switch kind {
	case store.LocationVideo:
		return "Video call — link in your calendar file"
	case store.LocationPhone:
		return "Phone call — details in your calendar file"
	}
	return locationTeaser(kind, value)
}

func confirmTZLabel(t time.Time, loc *time.Location) string {
	if zoneCity(loc) == "UTC" {
		return "UTC"
	}
	return zoneCity(loc) + " time (" + offsetLabel(t, loc) + ")"
}

func (s *Server) confirmPage(path string, back url.Values, start time.Time, d time.Duration, loc *time.Location, kind, location string) views.ConfirmPage {
	end := start.Add(d)
	owner := s.cfg.Owner.Name
	return views.ConfirmPage{
		Accent:          s.currentAccent(),
		Heading:         "Almost done",
		BackHref:        withQuery(path, back),
		DayLong:         dayLong(start.In(loc)),
		TimeRange:       timeRange(start, end, loc),
		OwnerName:       owner,
		LocationKind:    kind,
		LocationText:    locationBeforeBooking(kind, location),
		TZLabel:         confirmTZLabel(start, loc),
		OwnerTime:       s.ownerTime(start, end, loc),
		Action:          path,
		Start:           strconv.FormatInt(start.Unix(), 10),
		Minutes:         minutes(d),
		TZ:              loc.String(),
		AskDetails:      true,
		NotePlaceholder: "Anything " + owner + " should know",
		Submit:          "Book " + timeRange(start, end, loc),
		Footnote:        "No account, no email. Next you'll get a private link to change or cancel.",
	}
}

// backParams keeps the Guest on the same fortnight and day when going back.
func backParams(keep url.Values, loc *time.Location, q url.Values) url.Values {
	back := cloneValues(keep)
	back.Set("tz", loc.String())
	for _, k := range []string{"from", "day"} {
		if v := q.Get(k); v != "" {
			back.Set(k, v)
		}
	}
	return back
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	inv, err := s.svc.Invite(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	start, picked := parseStart(q.Get("start"))
	s.renderInvite(w, r, tok, inv, pickDuration(inv, q.Get("d")), s.guestZone(q), q, start, picked, http.StatusOK, "",
		booking.GuestDetails{Name: inv.Label})
}

// renderInvite shows the confirm form when a valid start is picked, and the
// Slot picker otherwise.
func (s *Server) renderInvite(w http.ResponseWriter, r *http.Request, tok string, inv store.Invite, d time.Duration, loc *time.Location, q url.Values, start time.Time, picked bool, status int, errMsg string, g booking.GuestDetails) {
	path := "/i/" + tok
	slots, err := s.svc.Slots(r.Context(), inv, d, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	keep := url.Values{"d": {minutes(d)}}
	if picked {
		if hasSlot(slots, start) {
			p := s.confirmPage(path, backParams(keep, loc, q), start, d, loc, inv.LocationKind, inv.Location)
			p.Name, p.Note, p.Error = g.Name, g.Note, errMsg
			s.render(w, r, status, views.Confirm(p))
			return
		}
		errMsg, status = slotTakenMsg, http.StatusConflict
	}
	var busy []schedule.Interval
	if inv.ShowBusy {
		if busy, err = s.svc.BusyBlocks(r.Context(), inv); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	from, to := s.guestWindow(inv)
	page := s.slotPicker(picker{path: path, keep: keep, loc: loc, q: q}, slots, from, to, busy, inv.ShowBusy)
	page.Accent = s.currentAccent()
	page.Title = "Book time with " + s.cfg.Owner.Name
	page.LocationKind = inv.LocationKind
	page.LocationLabel = locationTeaser(inv.LocationKind, inv.Location)
	page.DateRange = dateRangeLabel(from, to, loc)
	page.TZ.Label = tzLabel(s.svc.Now(), loc)
	page.Error = errMsg
	page.Footer = "No account needed. After booking you get a private link to change or cancel."
	if len(inv.Durations) > 1 {
		for _, opt := range inv.Durations {
			page.Durations = append(page.Durations, views.Choice{
				Label:  minutesLabel(opt),
				Href:   withQuery(path, url.Values{"d": {minutes(opt)}, "tz": {loc.String()}}),
				Active: opt == d,
			})
		}
	}
	s.render(w, r, status, views.Slots(page))
}

func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !s.allowBooking(w, r, tok) {
		return
	}
	inv, err := s.svc.Invite(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	loc := s.guestZone(url.Values{"tz": {r.PostFormValue("tz")}})
	d := pickDuration(inv, r.PostFormValue("d"))
	g := booking.GuestDetails{Name: r.PostFormValue("name"), Note: r.PostFormValue("note")}
	start, ok := parseStart(r.PostFormValue("start"))
	if !ok {
		s.renderInvite(w, r, tok, inv, d, loc, url.Values{}, time.Time{}, false, http.StatusBadRequest, "Please pick a time.", g)
		return
	}
	_, manage, err := s.svc.Book(r.Context(), tok, start, d, g)
	var inputErr *booking.InputError
	switch {
	case errors.As(err, &inputErr):
		s.renderInvite(w, r, tok, inv, d, loc, url.Values{}, start, true, http.StatusUnprocessableEntity, inputErr.Msg, g)
		return
	case errors.Is(err, booking.ErrSlotUnavailable):
		s.renderInvite(w, r, tok, inv, d, loc, url.Values{}, time.Time{}, false, http.StatusConflict, slotTakenMsg, g)
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
	base := "/b/" + tok
	tzq := url.Values{"tz": {loc.String()}}

	if q.Get("new") == "1" && !m.Cancelled() {
		s.render(w, r, http.StatusOK, views.Booked(views.BookedPage{
			Accent:       s.currentAccent(),
			GuestName:    m.GuestName,
			DayLong:      dayLong(m.Start.In(loc)),
			OwnerName:    s.cfg.Owner.Name,
			TimeRange:    timeRange(m.Start, m.End, loc),
			Offset:       offsetLabel(m.Start, loc),
			LocationText: locationTeaser(m.LocationKind, m.Location),
			ICSHref:      base + "/event.ics",
			ManageURL:    s.baseURL(r) + base,
			ManageHref:   withQuery(base, tzq),
		}))
		return
	}

	p := views.ManagePage{
		Accent:         s.currentAccent(),
		OwnerName:      s.cfg.Owner.Name,
		DayLong:        dayLong(m.Start.In(loc)),
		TimeRange:      timeRange(m.Start, m.End, loc),
		OwnerTime:      s.ownerTime(m.Start, m.End, loc),
		GuestName:      m.GuestName,
		LocationKind:   m.LocationKind,
		Location:       m.Location,
		LocationIsLink: isLink(m.Location),
		Note:           m.Note,
		TZ:             views.TZPicker{Label: tzLabel(s.svc.Now(), loc), Current: loc.String(), Choices: zoneChoices(loc.String()), Keep: map[string]string{}},
		Changeable:     m.Changeable,
		RescheduleHref: withQuery(base+"/reschedule", tzq),
		CancelAction:   withQuery(base+"/cancel", tzq),
	}
	if !m.Cancelled() {
		p.ICSHref = base + "/event.ics"
	}
	switch q.Get("changed") {
	case "moved":
		p.Notice = "Your Booking was moved."
	case "cancelled":
		p.Notice = "Your Booking is cancelled."
	}
	p.PillText, p.PillClass, p.StatusText = s.manageStatus(m, loc)
	s.render(w, r, http.StatusOK, views.Manage(p))
}

// manageStatus is the Manage page's pill and, for Bookings that can no longer
// change, the explanation.
func (s *Server) manageStatus(m booking.Managed, loc *time.Location) (pill, class, text string) {
	now, owner := s.svc.Now(), s.cfg.Owner.Name
	switch {
	case m.CancelledBy == store.CancelledByGuest:
		return "Cancelled by you", "pill-danger", "You cancelled this Booking. Ask " + owner + " for a new Invite if you'd like to meet another time."
	case m.CancelledBy == store.CancelledByOwner:
		return "Cancelled by " + owner, "pill-danger", owner + " cancelled this Booking and will be in touch."
	case !now.Before(m.End):
		gone := m.End.Add(booking.ManageLinkLifetime).In(loc).Format("2 January")
		return "Ended · read-only", "pill-neutral", "This Booking has ended, so it can't be changed. This page stops working on " + gone + "."
	case !now.Before(m.Start):
		return "Happening now", "pill-accent", "This Booking has started, so it can't be changed any more."
	default:
		return "Upcoming · " + relativeDay(now, m.Start, loc), "pill-accent", ""
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
	ev.Notes = strings.TrimSpace(m.Note + "\n\nReschedule or cancel: " + ev.URL)
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
		q.Set("changed", "cancelled")
	}
	http.Redirect(w, r, withQuery("/b/"+tok, q), http.StatusSeeOther)
}

func (s *Server) handleRescheduleForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start, picked := parseStart(q.Get("start"))
	s.renderReschedule(w, r, s.guestZone(q), q, start, picked, http.StatusOK, "")
}

func (s *Server) renderReschedule(w http.ResponseWriter, r *http.Request, loc *time.Location, q url.Values, start time.Time, picked bool, status int, errMsg string) {
	tok := r.PathValue("token")
	m, err := s.svc.Manage(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !m.Changeable {
		http.Redirect(w, r, withQuery("/b/"+tok, url.Values{"tz": {loc.String()}}), http.StatusSeeOther)
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
			p := s.confirmPage(path, backParams(url.Values{}, loc, q), start, d, loc, m.LocationKind, m.Location)
			p.Heading = "Move your Booking?"
			p.AskDetails = false
			p.Submit = "Move to " + timeRange(start, start.Add(d), loc)
			p.Footnote = "Your current time stays yours until you confirm."
			p.Error = errMsg
			s.render(w, r, status, views.Confirm(p))
			return
		}
		errMsg, status = slotTakenMsg, http.StatusConflict
	}
	from, to := s.guestWindow(m.Invite)
	page := s.slotPicker(picker{path: path, keep: url.Values{}, loc: loc, q: q}, slots, from, to, nil, false)
	page.Accent = s.currentAccent()
	page.Title = "Pick a new time"
	page.Intro = "Currently " + dayShort(m.Start.In(loc)) + ", " + timeRange(m.Start, m.End, loc) + "."
	page.LocationKind = m.LocationKind
	page.LocationLabel = locationTeaser(m.LocationKind, m.Location)
	page.DateRange = dateRangeLabel(from, to, loc)
	page.TZ.Label = tzLabel(s.svc.Now(), loc)
	page.Error = errMsg
	page.Footer = "Rescheduling keeps your Booking. You don't need a new Invite."
	s.render(w, r, status, views.Slots(page))
}

func (s *Server) handleReschedule(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !s.allowBooking(w, r, "") {
		return
	}
	loc := s.guestZone(url.Values{"tz": {r.PostFormValue("tz")}})
	start, ok := parseStart(r.PostFormValue("start"))
	if !ok {
		s.renderReschedule(w, r, loc, url.Values{}, time.Time{}, false, http.StatusBadRequest, "Please pick a time.")
		return
	}
	_, err := s.svc.Reschedule(r.Context(), tok, start)
	switch {
	case errors.Is(err, booking.ErrSlotUnavailable):
		s.renderReschedule(w, r, loc, url.Values{}, time.Time{}, false, http.StatusConflict, slotTakenMsg)
		return
	case errors.Is(err, booking.ErrLocked):
		http.Redirect(w, r, withQuery("/b/"+tok, url.Values{"tz": {loc.String()}}), http.StatusSeeOther)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, withQuery("/b/"+tok, url.Values{"changed": {"moved"}, "tz": {loc.String()}}), http.StatusSeeOther)
}

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
		events[i] = s.svc.OwnerEvent(b)
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	if err := ics.Write(w, "hima: bookings with "+s.cfg.Owner.Name, events...); err != nil {
		s.log.Error("write feed", "err", err)
	}
}
