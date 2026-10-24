# Roadmap

Terms follow [CONTEXT.md](../CONTEXT.md). Decisions: [docs/adr](./adr).

## MVP

- **Calendar sources**: multiple ICS URLs; recurrence (RRULE + exceptions) expansion; events marked free never block; all-day events block per-source setting; last good copy used for up to ~1h, then no Slots offered; refresh before confirming a Booking if cache is a few minutes old
- **Availability**: weekly hours, Owner time zone, buffers, minimum notice, booking horizon
- **Invites**: expiry (default 7 days), use count (default 1), allowed durations (default 30 min), date range, Label, Location, opt-in busy grid; states Active / Used up / Expired / Revoked; expiry, use count and Label editable after sharing
- **Guest booking page**: Slots in Guest's browser time zone (switchable), Guest name (pre-filled from Label) + optional note, `.ics` download, single neutral page for dead Invites
- **Manage link**: reschedule (does not consume an Invite use) and cancel; read-only after the Booking ends, disabled 30 days later
- **Owner admin**: create/revoke/edit Invites (link shown once), list Bookings, cancel, edit Location and notes, activity log
- **Owner feed**: secret ICS subscription of all Bookings (last 30 days onward)
- **Notifications**: ntfy + generic webhook, retried, never block a Booking
- **Auth**: mandatory `auth.mode` = `proxy` | `password`, fail closed (ADR 0002)
- **Storage & ops**: SQLite, hashed tokens, 90-day retention, single binary + Docker image (ADR 0003)

## Phase 2 — write-back

- CalDAV as Calendar source and write target (Radicale, Baïkal, iCloud, Fastmail)
- Rate limiting per Invite / per IP, daily Booking cap on Availability

## Phase 3 — big providers

- Google Calendar and Microsoft 365/Outlook together (OAuth read + write)
- Telegram notifications
- Passkeys / TOTP for password mode

## Later / maybe

- Named Availability profiles ("work", "weekends")
- Group Invites; federation between instances via free/busy exchange
- Separate admin/public ports
