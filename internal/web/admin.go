package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nikolaiianchuk/hima/internal/booking"
	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/schedule"
	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

// standardDurations are always offered in the new-Invite form, alongside the
// configured defaults.
var standardDurations = []time.Duration{15 * time.Minute, 30 * time.Minute, 45 * time.Minute, time.Hour, 90 * time.Minute, 2 * time.Hour}

var expiryChoices = []int{1, 3, 7, 14, 30}

// bookingHistory is how far back the admin lists past Bookings.
const bookingHistory = 30 * 24 * time.Hour

// datetimeLocal is the value format of <input type="datetime-local">.
const datetimeLocal = "2006-01-02T15:04"

func (s *Server) frame(current string) views.AdminFrame {
	return views.AdminFrame{
		Accent:    s.currentAccent(),
		OwnerName: s.cfg.Owner.Name,
		Current:   current,
		CanLogout: s.cfg.Auth.Mode == config.AuthModePassword,
	}
}

func (s *Server) ownerLoc() *time.Location { return s.svc.Availability.Location }

func (s *Server) sourceStatus() []calsource.SourceStatus {
	if st, ok := s.svc.Calendars.(sourceStatuser); ok {
		return st.Status()
	}
	return nil
}

func inviteState(st store.InviteState) (label, class string) {
	switch st {
	case store.InviteActive:
		return "Active", "pill-accent"
	case store.InviteUsedUp:
		return "Used up", "pill-neutral"
	case store.InviteExpired:
		return "Expired", "pill-warn"
	default:
		return "Revoked", "pill-danger"
	}
}

func parseID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// ---------- Invites ----------

func (s *Server) handleInvites(w http.ResponseWriter, r *http.Request) {
	now, loc := s.svc.Now(), s.ownerLoc()
	invites, err := s.store.ListInvites(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := views.InvitesPage{AdminFrame: s.frame("invites"), ActiveOnly: r.URL.Query().Get("filter") == "active", AllCount: len(invites)}
	p.Sources = s.sourceLine(now)
	for _, inv := range invites {
		state := inv.State(now)
		if state == store.InviteActive {
			p.ActiveCount++
		} else if p.ActiveOnly {
			continue
		}
		label, class := inviteState(state)
		expires := dateShort(inv.ExpiresAt.In(loc))
		switch state {
		case store.InviteRevoked:
			expires = "—"
		case store.InviteActive, store.InviteUsedUp:
			if now.Before(inv.ExpiresAt) {
				expires = untilLabel(now, inv.ExpiresAt)
			}
		}
		p.Rows = append(p.Rows, views.InviteRow{
			ID:        inv.ID,
			Label:     orDefault(inv.Label, "(no label)"),
			State:     label,
			PillClass: class,
			Uses:      fmt.Sprintf("%d / %d", inv.Uses, inv.MaxUses),
			Expires:   expires,
			Durations: durationsLabel(inv.Durations),
			Location:  locationSummary(inv.LocationKind, inv.Location),
			Active:    state == store.InviteActive,
		})
	}
	s.render(w, r, http.StatusOK, views.Invites(p))
}

// sourceLine summarises Calendar source health for the Invites page.
func (s *Server) sourceLine(now time.Time) views.SourceLine {
	statuses := s.sourceStatus()
	if len(statuses) == 0 {
		return views.SourceLine{OK: true, Text: "No calendar sources · all of your Availability is offered"}
	}
	var failing int
	var oldest time.Time
	for _, st := range statuses {
		if !st.Current {
			failing++
		}
		if oldest.IsZero() || st.LastSuccess.Before(oldest) {
			oldest = st.LastSuccess
		}
	}
	if failing > 0 {
		noun := "source isn't"
		if failing > 1 {
			noun = "sources aren't"
		}
		return views.SourceLine{Text: fmt.Sprintf("%d calendar %s current · Guests see no times", failing, noun)}
	}
	return views.SourceLine{OK: true, Text: "Calendar sources current · checked " + agoLabel(now, oldest)}
}

func (s *Server) newInviteForm() views.NewInviteForm {
	d := s.cfg.InviteDefaults
	kind := store.LocationVideo
	if d.Location != "" && !isLink(d.Location) {
		kind = store.LocationPlace
	}
	return views.NewInviteForm{
		ExpiresOptions: expiryOptions(d.ExpiresInDays),
		MaxUses:        d.MaxUses,
		Durations:      durationOptions(d.Durations),
		LocationKinds:  kindOptions(kind),
		Location:       d.Location,
	}
}

func expiryOptions(selected int) []views.Option {
	days := slices.Clone(expiryChoices)
	if !slices.Contains(days, selected) {
		days = append(days, selected)
		slices.Sort(days)
	}
	opts := make([]views.Option, len(days))
	for i, n := range days {
		label := fmt.Sprintf("%d days", n)
		if n == 1 {
			label = "1 day"
		}
		opts[i] = views.Option{Value: strconv.Itoa(n), Label: label, Selected: n == selected}
	}
	return opts
}

func durationOptions(checked []time.Duration) []views.DurationOption {
	all := append(slices.Clone(standardDurations), checked...)
	slices.Sort(all)
	all = slices.Compact(all)
	opts := make([]views.DurationOption, len(all))
	for i, d := range all {
		opts[i] = views.DurationOption{Minutes: int(d / time.Minute), Label: minutesLabel(d), Checked: slices.Contains(checked, d)}
	}
	return opts
}

func kindOptions(selected string) []views.Option {
	kinds := []struct{ value, label string }{
		{store.LocationPlace, "Place"},
		{store.LocationPhone, "Phone call"},
		{store.LocationVideo, "Video link"},
	}
	opts := make([]views.Option, len(kinds))
	for i, k := range kinds {
		opts[i] = views.Option{Value: k.value, Label: k.label, Selected: k.value == selected}
	}
	return opts
}

// availabilitySummary describes weekly hours compactly, e.g.
// "Mon–Thu 10:00–18:00, Fri 10:00–13:00".
func availabilitySummary(a schedule.Availability) string {
	order := []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}
	rangesOf := func(d time.Weekday) string {
		var parts []string
		for _, r := range a.Weekly[d] {
			parts = append(parts, r.From.String()+"–"+r.To.String())
		}
		return strings.Join(parts, ", ")
	}
	var groups []string
	for i := 0; i < len(order); {
		j := i
		for j+1 < len(order) && rangesOf(order[j+1]) == rangesOf(order[i]) {
			j++
		}
		if r := rangesOf(order[i]); r != "" {
			days := order[i].String()[:3]
			if j > i {
				days += "–" + order[j].String()[:3]
			}
			groups = append(groups, days+" "+r)
		}
		i = j + 1
	}
	if len(groups) == 0 {
		return "no hours"
	}
	return strings.Join(groups, ", ")
}

func (s *Server) newInvitePage(ctx context.Context, form views.NewInviteForm, errMsg string) views.NewInvitePage {
	a := s.svc.Availability
	hint := "Inside your Availability: " + availabilitySummary(a) + " (" + zoneCity(a.Location) + ")"
	if a.MinNotice > 0 {
		hint += ", " + durationLabel(a.MinNotice) + " notice"
	}
	if a.Buffer > 0 {
		hint += ", " + durationLabel(a.Buffer) + " buffers"
	}
	p := views.NewInvitePage{
		AdminFrame:       s.frame("invites"),
		Form:             form,
		Error:            errMsg,
		TZ:               zoneCity(a.Location),
		AvailabilityHint: hint + ".",
		PreviewTitle:     "Book time with " + s.cfg.Owner.Name,
	}
	defaults := s.cfg.InviteDefaults
	var parts []string
	for _, d := range defaults.Durations {
		parts = append(parts, strconv.Itoa(int(d/time.Minute)))
	}
	summary := strings.Join(parts, " or ") + " min"
	kind := store.LocationVideo
	if defaults.Location != "" && !isLink(defaults.Location) {
		kind = store.LocationPlace
	}
	summary += " · " + locationTeaser(kind, defaults.Location)
	summary += fmt.Sprintf(" · next %d days", int(a.Horizon.Hours()/24))
	p.PreviewSummary = summary
	slots, err := s.svc.Slots(ctx, store.Invite{Durations: defaults.Durations}, defaults.Durations[0], 0)
	switch {
	case errors.Is(err, calsource.ErrStale):
		p.PreviewSlots = "No times while calendars can't be read"
	case err != nil:
		s.log.Error("preview slots", "err", err)
	default:
		p.PreviewSlots = fmt.Sprintf("%d open Slots", len(slots))
	}
	return p
}

func (s *Server) handleNewInvite(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, views.NewInvite(s.newInvitePage(r.Context(), s.newInviteForm(), "")))
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	loc := s.ownerLoc()
	expiresDays, _ := strconv.Atoi(r.PostFormValue("expires_in_days"))
	maxUses, _ := strconv.Atoi(r.PostFormValue("max_uses"))
	var durations []time.Duration
	for _, v := range r.PostForm["durations"] {
		if m, err := strconv.Atoi(v); err == nil {
			durations = append(durations, time.Duration(m)*time.Minute)
		}
	}
	slices.Sort(durations)
	durations = slices.Compact(durations)
	kind := r.PostFormValue("location_kind")
	form := views.NewInviteForm{
		Label:          r.PostFormValue("label"),
		ExpiresOptions: expiryOptions(expiresDays),
		MaxUses:        maxUses,
		Durations:      durationOptions(durations),
		RangeStart:     r.PostFormValue("range_start"),
		RangeEnd:       r.PostFormValue("range_end"),
		LocationKinds:  kindOptions(kind),
		Location:       r.PostFormValue("location"),
		ShowBusy:       r.PostFormValue("show_busy") == "1",
	}
	n := booking.NewInvite{
		Label:        form.Label,
		Location:     form.Location,
		LocationKind: kind,
		Durations:    durations,
		MaxUses:      maxUses,
		ExpiresIn:    time.Duration(expiresDays) * 24 * time.Hour,
		ShowBusy:     form.ShowBusy,
	}
	var err error
	if n.RangeStart, err = parseDate(form.RangeStart, loc, 0); err == nil {
		// The end date is inclusive: the range runs to the following midnight.
		n.RangeEnd, err = parseDate(form.RangeEnd, loc, 1)
	}
	if err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, views.NewInvite(s.newInvitePage(r.Context(), form, "Dates must look like 2026-10-31.")))
		return
	}
	inv, tok, err := s.svc.CreateInvite(r.Context(), n)
	var inputErr *booking.InputError
	if errors.As(err, &inputErr) {
		s.render(w, r, http.StatusUnprocessableEntity, views.NewInvite(s.newInvitePage(r.Context(), form, inputErr.Msg)))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.invitePage(r.Context(), inv)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p.Link = s.baseURL(r) + "/i/" + tok
	s.render(w, r, http.StatusOK, views.Invite(p))
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

func (s *Server) invitePage(ctx context.Context, inv store.Invite) (views.InvitePage, error) {
	now, loc := s.svc.Now(), s.ownerLoc()
	state := inv.State(now)
	label, class := inviteState(state)
	bookings, err := s.store.BookingsForInvite(ctx, inv.ID)
	if err != nil {
		return views.InvitePage{}, err
	}
	dates := "Any time within your Availability"
	if !inv.RangeStart.IsZero() || !inv.RangeEnd.IsZero() {
		from, to := inv.RangeStart, inv.RangeEnd
		if from.IsZero() {
			from = inv.CreatedAt
		}
		if to.IsZero() {
			dates = "From " + dateShort(from.In(loc))
		} else {
			dates = dateRangeLabel(from, to, loc)
		}
	}
	where := locationSummary(inv.LocationKind, inv.Location)
	if inv.LocationKind != store.LocationPlace && inv.Location != "" {
		where += " · " + inv.Location
	}
	busy := "Hidden"
	if inv.ShowBusy {
		busy = "Shown"
	}
	p := views.InvitePage{
		AdminFrame:   s.frame("invites"),
		ID:           inv.ID,
		Title:        orDefault(inv.Label, "(no label)"),
		State:        label,
		PillClass:    class,
		Editable:     state != store.InviteRevoked,
		Label:        inv.Label,
		ExpiresInput: inv.ExpiresAt.In(loc).Format(datetimeLocal),
		MaxUses:      inv.MaxUses,
		MinUses:      max(inv.Uses, 1),
		Fixed: []views.KV{
			{Key: "Durations", Value: durationsLabel(inv.Durations)},
			{Key: "Dates", Value: dates},
			{Key: "Location", Value: where},
			{Key: "Busy times", Value: busy},
		},
		UsedText:  fmt.Sprintf("%d / %d used", inv.Uses, inv.MaxUses),
		EmptyText: "None yet. You'll get a notification when someone books.",
		Revocable: state != store.InviteRevoked,
	}
	for _, b := range bookings {
		p.Bookings = append(p.Bookings, views.InviteBooking{
			When:   whenLabel(b.Start, b.End, loc),
			Name:   b.GuestName,
			Status: ownerStatus(b, now),
		})
	}
	return p, nil
}

func (s *Server) handleInviteDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.handleNotFound(w, r)
		return
	}
	inv, err := s.store.InviteByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.invitePage(r.Context(), inv)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p.Saved = r.URL.Query().Get("saved") == "1"
	s.render(w, r, http.StatusOK, views.Invite(p))
}

func (s *Server) handleEditInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.handleNotFound(w, r)
		return
	}
	fail := func(msg string) {
		inv, err := s.store.InviteByID(r.Context(), id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		p, err := s.invitePage(r.Context(), inv)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		p.Error = msg
		s.render(w, r, http.StatusUnprocessableEntity, views.Invite(p))
	}
	maxUses, _ := strconv.Atoi(r.PostFormValue("max_uses"))
	expires, err := time.ParseInLocation(datetimeLocal, r.PostFormValue("expires_at"), s.ownerLoc())
	if err != nil {
		fail("Please give a valid expiry date and time.")
		return
	}
	err = s.svc.EditInvite(r.Context(), id, booking.InviteEdit{Label: r.PostFormValue("label"), MaxUses: maxUses, ExpiresAt: expires})
	var inputErr *booking.InputError
	switch {
	case errors.As(err, &inputErr):
		fail(inputErr.Msg)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/invites/"+strconv.FormatInt(id, 10)+"?saved=1", http.StatusSeeOther)
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.handleNotFound(w, r)
		return
	}
	if err := s.store.RevokeInvite(r.Context(), id, s.svc.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/invites/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ---------- Bookings ----------

func ownerStatus(b store.Booking, now time.Time) string {
	switch {
	case b.CancelledBy == store.CancelledByGuest:
		return "Cancelled by Guest"
	case b.CancelledBy == store.CancelledByOwner:
		return "Cancelled by you"
	case !now.Before(b.End):
		return "Ended"
	case b.Reschedules > 0:
		return "Rescheduled"
	default:
		return "Confirmed"
	}
}

func bookingTag(b store.Booking, now time.Time) (tag, class string) {
	switch {
	case b.Cancelled():
		return ownerStatus(b, now), "pill-danger"
	case !now.Before(b.End):
		return "Ended", "pill-neutral"
	case !now.Before(b.Start):
		return "Now", "pill-accent"
	case b.Reschedules > 0:
		return "Rescheduled", "pill-accent"
	}
	return "", ""
}

func (s *Server) handleBookings(w http.ResponseWriter, r *http.Request) {
	s.renderBookings(w, r, http.StatusOK, "", "")
}

func (s *Server) renderBookings(w http.ResponseWriter, r *http.Request, status int, newFeedURL, errMsg string) {
	ctx := r.Context()
	now, loc := s.svc.Now(), s.ownerLoc()
	bookings, err := s.store.ListBookings(ctx, now.Add(-bookingHistory))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := views.BookingsPage{AdminFrame: s.frame("bookings"), Error: errMsg, Feed: views.FeedBox{NewURL: newFeedURL}}
	if since, err := s.svc.FeedCreatedAt(ctx); err != nil {
		s.fail(w, r, err)
		return
	} else if !since.IsZero() {
		p.Feed.Active = true
		p.Feed.Since = dateShort(since.In(loc))
	}
	for _, st := range s.sourceStatus() {
		if st.Current {
			continue
		}
		if st.LastSuccess.IsZero() {
			p.Alert = st.Name + " couldn't be read yet. Guests see no Slots until it can."
		} else {
			p.Alert = st.Name + " hasn't been read for " + strings.TrimSuffix(agoLabel(now, st.LastSuccess), " ago") + ". Guests see no Slots until it can."
		}
		break
	}
	for _, b := range bookings {
		tag, class := bookingTag(b, now)
		invite := "No label"
		if b.InviteLabel != "" {
			invite = "Invite “" + b.InviteLabel + "”"
		}
		past := b.Cancelled() || !now.Before(b.End)
		row := views.BookingRow{
			ID:           b.ID,
			Day:          dayShort(b.Start.In(loc)),
			Time:         clockLabel(b.Start.In(loc)) + "–" + clockLabel(b.End.In(loc)),
			Name:         b.GuestName,
			Tag:          tag,
			TagClass:     class,
			Invite:       invite,
			Location:     locationSummary(b.LocationKind, b.Location),
			Note:         b.Note,
			Past:         past,
			Cancellable:  !b.Cancelled() && now.Before(b.Start),
			Editable:     !past,
			EditLocation: b.Location,
			EditNote:     b.Note,
		}
		if past {
			p.Past = append(p.Past, row)
		} else {
			p.Upcoming = append(p.Upcoming, row)
		}
	}
	slices.Reverse(p.Past)
	s.render(w, r, status, views.Bookings(p))
}

func (s *Server) handleEditBooking(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.handleNotFound(w, r)
		return
	}
	err := s.svc.EditBooking(r.Context(), id, r.PostFormValue("location"), r.PostFormValue("note"))
	var inputErr *booking.InputError
	switch {
	case errors.As(err, &inputErr):
		s.renderBookings(w, r, http.StatusUnprocessableEntity, "", inputErr.Msg)
		return
	case errors.Is(err, booking.ErrLocked):
		s.renderBookings(w, r, http.StatusConflict, "", "That Booking is cancelled or over and can't be changed.")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/bookings", http.StatusSeeOther)
}

func (s *Server) handleOwnerCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.handleNotFound(w, r)
		return
	}
	if err := s.svc.CancelByOwner(r.Context(), id); err != nil && !errors.Is(err, booking.ErrLocked) {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/bookings", http.StatusSeeOther)
}

func (s *Server) handleRotateFeed(w http.ResponseWriter, r *http.Request) {
	tok, err := s.svc.RotateFeed(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderBookings(w, r, http.StatusOK, s.baseURL(r)+"/feed/"+tok+".ics", "")
}

// ---------- Availability, sources, activity ----------

func (s *Server) handleAvailability(w http.ResponseWriter, r *http.Request) {
	a := s.svc.Availability
	p := views.AvailabilityPage{AdminFrame: s.frame("availability")}
	for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday} {
		var parts []string
		for _, rg := range a.Weekly[d] {
			parts = append(parts, rg.From.String()+"–"+rg.To.String())
		}
		p.Weekly = append(p.Weekly, views.KV{Key: d.String(), Value: orDefault(strings.Join(parts, ", "), "Unavailable")})
	}
	none := func(d time.Duration) string {
		if d == 0 {
			return "None"
		}
		return durationLabel(d)
	}
	p.Rules = []views.KV{
		{Key: "Time zone", Value: tzLabel(s.svc.Now(), a.Location)},
		{Key: "Buffer", Value: none(a.Buffer)},
		{Key: "Minimum notice", Value: none(a.MinNotice)},
		{Key: "Book ahead", Value: fmt.Sprintf("%d days", int(math.Round(a.Horizon.Hours()/24)))},
		{Key: "Start times every", Value: durationLabel(a.Step)},
		{Key: "Bookings per day", Value: perDay(a.MaxPerDay)},
	}
	s.render(w, r, http.StatusOK, views.Availability(p))
}

func perDay(n int) string {
	if n <= 0 {
		return "No limit"
	}
	return fmt.Sprintf("At most %d", n)
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	now, loc := s.svc.Now(), s.ownerLoc()
	p := views.SourcesPage{AdminFrame: s.frame("sources")}
	for _, st := range s.sourceStatus() {
		row := views.SourceRow{Name: st.Name, AllDay: "Ignored"}
		if st.AllDayBlocks {
			row.AllDay = "Block the whole day"
		}
		switch {
		case st.LastSuccess.IsZero():
			row.State, row.PillClass, row.Checked = "Not read yet", "pill-warn", "Never"
		case st.Current:
			row.State, row.PillClass = "Current", "pill-accent"
		default:
			row.State, row.PillClass = "Not current", "pill-danger"
		}
		if !st.LastSuccess.IsZero() {
			row.Checked = agoLabel(now, st.LastSuccess) + " (" + dayShort(st.LastSuccess.In(loc)) + ", " + clockLabel(st.LastSuccess.In(loc)) + ")"
		}
		if st.LastError != nil {
			row.Error = st.LastError.Error()
		}
		p.Sources = append(p.Sources, row)
	}
	s.render(w, r, http.StatusOK, views.Sources(p))
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	loc := s.ownerLoc()
	items, err := s.store.RecentActivity(r.Context(), 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := views.ActivityPage{AdminFrame: s.frame("activity")}
	for _, a := range items {
		p.Items = append(p.Items, views.ActivityItem{
			When:    dayShort(a.At.In(loc)) + " " + clockLabel(a.At.In(loc)),
			Title:   a.Title,
			Message: a.Message,
		})
	}
	s.render(w, r, http.StatusOK, views.Activity(p))
}

// ---------- Settings ----------

var accentPresets = []struct{ Name, Hex string }{
	{"Indigo", "#2D3E8F"}, {"Pine", "#1F5F5B"}, {"Moss", "#4A6125"}, {"Ochre", "#85570A"},
	{"Rust", "#A3401F"}, {"Rose", "#A3324F"}, {"Plum", "#6B3A7A"}, {"Graphite", "#37404A"},
}

// contrastOnWhite is the WCAG contrast ratio of hex against white.
func contrastOnWhite(hex string) float64 {
	n, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		return 0
	}
	lin := func(c uint64) float64 {
		v := float64(c) / 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	l := 0.2126*lin(n>>16&255) + 0.7152*lin(n>>8&255) + 0.0722*lin(n&255)
	return 1.05 / (l + 0.05)
}

// validAccent accepts #RRGGBB colours dark enough for white button text.
func validAccent(v string) bool {
	return hexColorRe.MatchString(v) && contrastOnWhite(v) >= 4.5
}

func (s *Server) settingsPage(custom, errMsg string, saved bool) views.SettingsPage {
	current := s.currentAccent()
	p := views.SettingsPage{AdminFrame: s.frame("settings"), Custom: custom, Error: errMsg, Saved: saved}
	preset := false
	for _, ps := range accentPresets {
		on := strings.EqualFold(ps.Hex, current)
		preset = preset || on
		p.Swatches = append(p.Swatches, views.Swatch{Name: ps.Name, Hex: ps.Hex, Checked: on && custom == ""})
	}
	if !preset && custom == "" {
		p.Custom = current
	}
	return p
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, views.Settings(s.settingsPage("", "", r.URL.Query().Get("saved") == "1")))
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	v := strings.TrimSpace(r.PostFormValue("custom"))
	if v == "" {
		v = r.PostFormValue("accent")
	} else if !strings.HasPrefix(v, "#") {
		v = "#" + v
	}
	switch {
	case !hexColorRe.MatchString(v):
		s.render(w, r, http.StatusUnprocessableEntity, views.Settings(s.settingsPage(r.PostFormValue("custom"), "Use a hex colour like #2D3E8F.", false)))
		return
	case contrastOnWhite(v) < 4.5:
		msg := fmt.Sprintf("Too light: %.1f:1 against white. Pick a darker colour.", contrastOnWhite(v))
		s.render(w, r, http.StatusUnprocessableEntity, views.Settings(s.settingsPage(r.PostFormValue("custom"), msg, false)))
		return
	}
	if err := s.setAccent(r.Context(), strings.ToUpper(v)); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, withQuery("/admin/settings", url.Values{"saved": {"1"}}), http.StatusSeeOther)
}
