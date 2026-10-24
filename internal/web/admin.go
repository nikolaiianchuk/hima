package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nikolaiianchuk/hima/internal/booking"
	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

// standardDurations are always offered in the new-Invite form, alongside the
// configured defaults.
var standardDurations = []time.Duration{15 * time.Minute, 30 * time.Minute, 45 * time.Minute, time.Hour, 90 * time.Minute, 2 * time.Hour}

// recentBookings is how far back the admin lists past Bookings.
const recentBookings = 7 * 24 * time.Hour

func (s *Server) defaultForm() views.NewInviteForm {
	d := s.cfg.InviteDefaults
	return views.NewInviteForm{
		Location:      d.Location,
		Durations:     durationOptions(d.Durations),
		MaxUses:       d.MaxUses,
		ExpiresInDays: d.ExpiresInDays,
	}
}

func durationOptions(checked []time.Duration) []views.DurationOption {
	all := append(slices.Clone(standardDurations), checked...)
	slices.Sort(all)
	all = slices.Compact(all)
	opts := make([]views.DurationOption, len(all))
	for i, d := range all {
		opts[i] = views.DurationOption{Minutes: int(d / time.Minute), Label: durationLabel(d), Checked: slices.Contains(checked, d)}
	}
	return opts
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	s.renderAdmin(w, r, http.StatusOK, s.defaultForm(), "", "")
}

// renderAdmin shows the admin page. formErr belongs to the new-Invite form,
// editErr to an edit of an existing Invite or Booking.
func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, status int, form views.NewInviteForm, formErr, editErr string) {
	ctx := r.Context()
	now := s.svc.Now()
	loc := s.svc.Availability.Location

	invites, err := s.store.ListInvites(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	bookings, err := s.store.ListBookings(ctx, now.Add(-recentBookings))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page := views.AdminPage{
		OwnerName: s.cfg.Owner.Name,
		TZ:        loc.String(),
		CanLogout: s.cfg.Auth.Mode == config.AuthModePassword,
		Error:     formErr,
		EditError: editErr,
		Form:      form,
	}
	if since, err := s.svc.FeedCreatedAt(ctx); err != nil {
		s.fail(w, r, err)
		return
	} else if !since.IsZero() {
		page.FeedSince = dayLabel(since.In(loc))
	}
	activity, err := s.store.RecentActivity(ctx, 20)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, a := range activity {
		page.Activity = append(page.Activity, views.ActivityItem{
			When:    dayLabel(a.At.In(loc)) + " " + clockLabel(a.At.In(loc)),
			Title:   a.Title,
			Message: a.Message,
		})
	}
	if _, err := s.svc.Calendars.Busy(now, now.Add(time.Minute)); err != nil {
		page.Warning = "Your calendars couldn't be read recently, so guests currently see no free times."
	}
	for _, inv := range invites {
		var ds []string
		for _, d := range inv.Durations {
			ds = append(ds, durationLabel(d))
		}
		state := inv.State(now)
		page.Invites = append(page.Invites, views.AdminInvite{
			ID:           inv.ID,
			Label:        orDefault(inv.Label, "(no label)"),
			RawLabel:     inv.Label,
			Editable:     state != store.InviteRevoked,
			MaxUses:      inv.MaxUses,
			UsedCount:    inv.Uses,
			ExpiresInput: inv.ExpiresAt.In(loc).Format(datetimeLocal),
			State:        string(state),
			Uses:         fmt.Sprintf("%d / %d", inv.Uses, inv.MaxUses),
			Expires:      dayLabel(inv.ExpiresAt.In(loc)) + " " + clockLabel(inv.ExpiresAt.In(loc)),
			Durations:    strings.Join(ds, ", "),
			Location:     inv.Location,
			Active:       state == store.InviteActive,
		})
	}
	for _, b := range bookings {
		page.Bookings = append(page.Bookings, views.AdminBooking{
			ID:          b.ID,
			When:        whenLabel(b.Start, b.End, loc),
			GuestName:   b.GuestName,
			InviteLabel: b.InviteLabel,
			Note:        b.Note,
			Location:    b.Location,
			Status:      ownerStatus(b, now),
			Cancellable: !b.Cancelled() && now.Before(b.Start),
			Editable:    !b.Cancelled() && now.Before(b.End),
		})
	}
	s.render(w, r, status, views.Admin(page))
}

func ownerStatus(b store.Booking, now time.Time) string {
	switch {
	case b.CancelledBy == store.CancelledByGuest:
		return "Cancelled by guest"
	case b.CancelledBy == store.CancelledByOwner:
		return "Cancelled by you"
	case !now.Before(b.End):
		return "Past"
	default:
		return "Confirmed"
	}
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	loc := s.svc.Availability.Location
	form := views.NewInviteForm{
		Label:      r.PostFormValue("label"),
		Location:   r.PostFormValue("location"),
		RangeStart: r.PostFormValue("range_start"),
		RangeEnd:   r.PostFormValue("range_end"),
		ShowBusy:   r.PostFormValue("show_busy") == "1",
	}
	form.MaxUses, _ = strconv.Atoi(r.PostFormValue("max_uses"))
	form.ExpiresInDays, _ = strconv.Atoi(r.PostFormValue("expires_in_days"))
	var durations []time.Duration
	for _, v := range r.PostForm["durations"] {
		if m, err := strconv.Atoi(v); err == nil {
			durations = append(durations, time.Duration(m)*time.Minute)
		}
	}
	slices.Sort(durations)
	durations = slices.Compact(durations)
	form.Durations = durationOptions(durations)

	n := booking.NewInvite{
		Label:     form.Label,
		Location:  form.Location,
		Durations: durations,
		MaxUses:   form.MaxUses,
		ExpiresIn: time.Duration(form.ExpiresInDays) * 24 * time.Hour,
		ShowBusy:  form.ShowBusy,
	}
	var err error
	if n.RangeStart, err = parseDate(form.RangeStart, loc, 0); err == nil {
		// The end date is inclusive: the range runs to the following midnight.
		n.RangeEnd, err = parseDate(form.RangeEnd, loc, 1)
	}
	if err != nil {
		s.renderAdmin(w, r, http.StatusUnprocessableEntity, form, "Dates must look like 2026-10-31.", "")
		return
	}

	inv, tok, err := s.svc.CreateInvite(r.Context(), n)
	var inputErr *booking.InputError
	if errors.As(err, &inputErr) {
		s.renderAdmin(w, r, http.StatusUnprocessableEntity, form, inputErr.Msg, "")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var ds []string
	for _, d := range inv.Durations {
		ds = append(ds, durationLabel(d))
	}
	uses := "1 use"
	if inv.MaxUses != 1 {
		uses = fmt.Sprintf("%d uses", inv.MaxUses)
	}
	s.render(w, r, http.StatusOK, views.InviteCreated(views.InviteCreatedPage{
		URL:     s.baseURL(r) + "/i/" + tok,
		Label:   inv.Label,
		Summary: fmt.Sprintf("%s · %s · expires %s", strings.Join(ds, " or "), uses, dayLabel(inv.ExpiresAt.In(loc))+" "+clockLabel(inv.ExpiresAt.In(loc))),
	}))
}

// parseDate parses an optional YYYY-MM-DD as midnight in loc, plus addDays.
func parseDate(v string, loc *time.Location, addDays int) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", v, loc)
	if err != nil {
		return time.Time{}, err
	}
	return t.AddDate(0, 0, addDays), nil
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	if err := s.store.RevokeInvite(r.Context(), id, s.svc.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleOwnerCancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	if err := s.svc.CancelByOwner(r.Context(), id); err != nil && !errors.Is(err, booking.ErrLocked) {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// datetimeLocal is the value format of <input type="datetime-local">.
const datetimeLocal = "2006-01-02T15:04"

func (s *Server) handleEditInvite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	maxUses, _ := strconv.Atoi(r.PostFormValue("max_uses"))
	expires, err := time.ParseInLocation(datetimeLocal, r.PostFormValue("expires_at"), s.svc.Availability.Location)
	if err != nil {
		s.renderAdmin(w, r, http.StatusUnprocessableEntity, s.defaultForm(), "", "Please give a valid expiry date and time.")
		return
	}
	err = s.svc.EditInvite(r.Context(), id, booking.InviteEdit{Label: r.PostFormValue("label"), MaxUses: maxUses, ExpiresAt: expires})
	var inputErr *booking.InputError
	switch {
	case errors.As(err, &inputErr):
		s.renderAdmin(w, r, http.StatusUnprocessableEntity, s.defaultForm(), "", inputErr.Msg)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleEditBooking(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	err = s.svc.EditBooking(r.Context(), id, r.PostFormValue("location"), r.PostFormValue("note"))
	var inputErr *booking.InputError
	switch {
	case errors.As(err, &inputErr):
		s.renderAdmin(w, r, http.StatusUnprocessableEntity, s.defaultForm(), "", inputErr.Msg)
		return
	case errors.Is(err, booking.ErrLocked):
		s.renderAdmin(w, r, http.StatusConflict, s.defaultForm(), "", "That booking is cancelled or over and can't be changed.")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}
