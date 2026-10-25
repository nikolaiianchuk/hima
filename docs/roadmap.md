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

## MVP finish

- Edit an Invite after sharing (expiry, use count, Label)
- Edit a Booking's Location and note from the admin
- Retention: purge past Bookings, dead Invites and old activity after 90 days
- Deployment readiness: Docker image check, README, reverse-proxy guide
- ~~Week-grid view of Busy blocks for Invites that opt in~~ (done as a per-day busy bar in the new design)
- ~~Align the UI with the Owner's design~~ (done)

## Phase 2 — write-back (done)

- ~~CalDAV as Calendar source and write target (Radicale, Baïkal, iCloud, Fastmail)~~
- ~~Rate limiting per Invite / per IP, daily Booking cap on Availability~~

## Phase 3 — big providers (done, except passkeys)

- ~~Google Calendar and Microsoft 365/Outlook together (OAuth read + write)~~
- ~~Telegram notifications~~
- ~~TOTP for password mode~~
- Passkeys for password mode (deferred: proxy mode with Authentik already offers them)

## Since then

- ~~Email invitations to the Owner as a write-back option (SMTP)~~

## Later / maybe

- Named Availability profiles ("work", "weekends")
- Group Invites; federation between instances via free/busy exchange
- Separate admin/public ports
