package ics

import (
	"strings"
	"testing"
	"time"
)

var lisbon, _ = time.LoadLocation("Europe/Lisbon")

func feed(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		strings.Join(events, "") +
		"END:VCALENDAR\r\n"
}

func event(lines ...string) string {
	return "BEGIN:VEVENT\r\nDTSTAMP:20260101T000000Z\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, lisbon)
	if err != nil {
		panic(err)
	}
	return t
}

const me = "Me@Example.com"

func busy(t *testing.T, ics string, from, to string, allDay bool) []string {
	t.Helper()
	cal, err := Parse(strings.NewReader(ics), Options{Location: lisbon, OwnerEmails: []string{me, "alias@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if cal.Skipped != 0 {
		t.Fatalf("skipped %d events", cal.Skipped)
	}
	var out []string
	for _, iv := range cal.Busy(at(from), at(to), allDay) {
		out = append(out, iv.Start.In(lisbon).Format("01-02 15:04")+"–"+iv.End.In(lisbon).Format("01-02 15:04"))
	}
	return out
}

func assertBusy(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("busy = %v, want %v", got, want)
	}
}

func TestSingleEvents(t *testing.T) {
	ics := feed(
		event("UID:utc", "DTSTART:20261026T090000Z", "DTEND:20261026T100000Z"),
		event("UID:tz", "DTSTART;TZID=America/New_York:20261026T090000", "DURATION:PT30M"),
		event("UID:floating", "DTSTART:20261027T150000", "DTEND:20261027T160000"),
		event("UID:outside", "DTSTART:20261101T090000Z", "DTEND:20261101T100000Z"),
	)
	assertBusy(t, busy(t, ics, "2026-10-26 00:00", "2026-10-28 00:00", false),
		"10-26 09:00–10-26 10:00", // UTC == Lisbon winter time
		"10-26 13:00–10-26 13:30", // 09:00 New York (EDT until Nov 1)
		"10-27 15:00–10-27 16:00", // floating, read in the Owner's zone
	)
}

func TestFreeAndCancelledNeverBlock(t *testing.T) {
	ics := feed(
		event("UID:free", "DTSTART:20261026T090000Z", "DTEND:20261026T100000Z", "TRANSP:TRANSPARENT"),
		event("UID:cancelled", "DTSTART:20261026T110000Z", "DTEND:20261026T120000Z", "STATUS:CANCELLED"),
		event("UID:zero", "DTSTART:20261026T130000Z", "DTEND:20261026T130000Z"),
	)
	assertBusy(t, busy(t, ics, "2026-10-26 00:00", "2026-10-27 00:00", false))
}

func TestAllDayBlocksOnlyWhenAsked(t *testing.T) {
	ics := feed(event("UID:holiday", "DTSTART;VALUE=DATE:20261026", "DTEND;VALUE=DATE:20261028"))
	assertBusy(t, busy(t, ics, "2026-10-25 00:00", "2026-10-30 00:00", false))
	assertBusy(t, busy(t, ics, "2026-10-25 00:00", "2026-10-30 00:00", true), "10-26 00:00–10-28 00:00")
}

func TestAllDayAcrossDSTIsStillWholeDays(t *testing.T) {
	// 2026-10-25 is 25 hours long in Lisbon.
	ics := feed(event("UID:d", "DTSTART;VALUE=DATE:20261025", "DTEND;VALUE=DATE:20261026", "RRULE:FREQ=DAILY;COUNT=2"))
	assertBusy(t, busy(t, ics, "2026-10-24 00:00", "2026-10-28 00:00", true),
		"10-25 00:00–10-26 00:00", "10-26 00:00–10-27 00:00")
}

func TestWeeklyKeepsWallClockAcrossDST(t *testing.T) {
	ics := feed(event("UID:gym", "DTSTART;TZID=Europe/Lisbon:20261020T190000", "DTEND;TZID=Europe/Lisbon:20261020T200000",
		"RRULE:FREQ=WEEKLY;BYDAY=TU"))
	assertBusy(t, busy(t, ics, "2026-10-19 00:00", "2026-11-01 00:00", false),
		"10-20 19:00–10-20 20:00", "10-27 19:00–10-27 20:00")
}

func TestExdateWithSeveralValues(t *testing.T) {
	ics := feed(event("UID:standup", "DTSTART:20261026T090000Z", "DTEND:20261026T091500Z",
		"RRULE:FREQ=DAILY;COUNT=5", "EXDATE:20261027T090000Z,20261029T090000Z"))
	assertBusy(t, busy(t, ics, "2026-10-26 00:00", "2026-11-02 00:00", false),
		"10-26 09:00–10-26 09:15", "10-28 09:00–10-28 09:15", "10-30 09:00–10-30 09:15")
}

func TestMovedAndCancelledInstances(t *testing.T) {
	ics := feed(
		event("UID:1on1", "DTSTART:20261026T140000Z", "DTEND:20261026T150000Z", "RRULE:FREQ=DAILY;COUNT=3"),
		// Tuesday moved to 16:00.
		event("UID:1on1", "RECURRENCE-ID:20261027T140000Z", "DTSTART:20261027T160000Z", "DTEND:20261027T170000Z"),
		// Wednesday cancelled.
		event("UID:1on1", "RECURRENCE-ID:20261028T140000Z", "DTSTART:20261028T140000Z", "DTEND:20261028T150000Z", "STATUS:CANCELLED"),
	)
	assertBusy(t, busy(t, ics, "2026-10-26 00:00", "2026-10-30 00:00", false),
		"10-26 14:00–10-26 15:00", "10-27 16:00–10-27 17:00")
}

func TestOccurrenceStartingBeforeWindowStillCounts(t *testing.T) {
	ics := feed(event("UID:long", "DTSTART:20261026T080000Z", "DTEND:20261026T120000Z", "RRULE:FREQ=DAILY"))
	assertBusy(t, busy(t, ics, "2026-10-26 10:00", "2026-10-26 11:00", false), "10-26 08:00–10-26 12:00")
}

func TestWindowsTimeZoneName(t *testing.T) {
	ics := feed(event("UID:outlook", "DTSTART;TZID=W. Europe Standard Time:20261026T100000",
		"DTEND;TZID=W. Europe Standard Time:20261026T110000"))
	// 10:00 Berlin is 09:00 Lisbon.
	assertBusy(t, busy(t, ics, "2026-10-26 00:00", "2026-10-27 00:00", false), "10-26 09:00–10-26 10:00")
}

func TestUnknownTimeZoneIsFloating(t *testing.T) {
	ics := feed(event("UID:custom", "DTSTART;TZID=Customized Time Zone:20261026T100000",
		"DTEND;TZID=Customized Time Zone:20261026T110000"))
	assertBusy(t, busy(t, ics, "2026-10-26 00:00", "2026-10-27 00:00", false), "10-26 10:00–10-26 11:00")
}

func TestBrokenEventIsSkippedNotFatal(t *testing.T) {
	ics := feed(
		event("UID:bad", "DTSTART:not-a-date", "DTEND:20261026T110000Z"),
		event("UID:good", "DTSTART:20261026T120000Z", "DTEND:20261026T130000Z"),
	)
	cal, err := Parse(strings.NewReader(ics), Options{Location: lisbon})
	if err != nil {
		t.Fatal(err)
	}
	if cal.Skipped != 1 || len(cal.Busy(at("2026-10-26 00:00"), at("2026-10-27 00:00"), false)) != 1 {
		t.Errorf("Skipped = %d", cal.Skipped)
	}
}

func TestOwnerReplies(t *testing.T) {
	ics := feed(
		event("UID:declined", "DTSTART:20261026T090000Z", "DTEND:20261026T100000Z",
			"ATTENDEE;PARTSTAT=ACCEPTED:mailto:boss@example.com",
			"ATTENDEE;PARTSTAT=DECLINED:MAILTO:me@example.com"),
		event("UID:accepted", "DTSTART:20261026T110000Z", "DTEND:20261026T120000Z",
			"ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com"),
		event("UID:unanswered", "DTSTART:20261026T130000Z", "DTEND:20261026T140000Z",
			"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:me@example.com"),
		event("UID:tentative", "DTSTART:20261026T150000Z", "DTEND:20261026T160000Z",
			"ATTENDEE;PARTSTAT=TENTATIVE:mailto:me@example.com"),
		// Someone else declining says nothing about the Owner.
		event("UID:other-declined", "DTSTART:20261026T170000Z", "DTEND:20261026T180000Z",
			"ATTENDEE;PARTSTAT=DECLINED:mailto:bob@example.com"),
		// Declined with one address but accepted with another still blocks.
		event("UID:mixed", "DTSTART:20261027T090000Z", "DTEND:20261027T100000Z",
			"ATTENDEE;PARTSTAT=DECLINED:mailto:me@example.com",
			"ATTENDEE;PARTSTAT=ACCEPTED:mailto:alias@example.com"),
		// Address given only in the EMAIL parameter.
		event("UID:email-param", "DTSTART:20261027T110000Z", "DTEND:20261027T120000Z",
			"ATTENDEE;PARTSTAT=DECLINED;EMAIL=alias@example.com:urn:uuid:1234"),
	)
	assertBusy(t, busy(t, ics, "2026-10-26 00:00", "2026-10-28 00:00", false),
		"10-26 11:00–10-26 12:00", "10-26 13:00–10-26 14:00", "10-26 15:00–10-26 16:00",
		"10-26 17:00–10-26 18:00", "10-27 09:00–10-27 10:00")
}

func TestDeclinedSingleOccurrence(t *testing.T) {
	ics := feed(
		event("UID:weekly", "DTSTART:20261026T140000Z", "DTEND:20261026T150000Z", "RRULE:FREQ=DAILY;COUNT=3",
			"ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com"),
		event("UID:weekly", "RECURRENCE-ID:20261027T140000Z", "DTSTART:20261027T140000Z", "DTEND:20261027T150000Z",
			"ATTENDEE;PARTSTAT=DECLINED:mailto:me@example.com"),
	)
	assertBusy(t, busy(t, ics, "2026-10-26 00:00", "2026-10-30 00:00", false),
		"10-26 14:00–10-26 15:00", "10-28 14:00–10-28 15:00")
}

func TestWithoutOwnerEmailsDeclinedStillBlocks(t *testing.T) {
	ics := feed(event("UID:d", "DTSTART:20261026T090000Z", "DTEND:20261026T100000Z",
		"ATTENDEE;PARTSTAT=DECLINED:mailto:me@example.com"))
	cal, err := Parse(strings.NewReader(ics), Options{Location: lisbon})
	if err != nil {
		t.Fatal(err)
	}
	if got := cal.Busy(at("2026-10-26 00:00"), at("2026-10-27 00:00"), false); len(got) != 1 {
		t.Errorf("busy = %v, want the event to block", got)
	}
}
