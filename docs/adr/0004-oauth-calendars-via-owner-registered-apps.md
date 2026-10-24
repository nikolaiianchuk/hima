# Google and Microsoft calendars via Owner-registered OAuth apps

hima connects to Google Calendar and Microsoft 365 through OAuth apps the Owner registers themselves (client ID and secret in config), because a self-hosted instance cannot ship shared credentials and each instance has its own redirect URI. Busy time is read through each provider's free/busy API (Google `freeBusy`, Microsoft `getSchedule`) rather than by downloading events: it returns only intervals, never titles, and the provider already resolves recurrence, declined invitations and "show as free". Refresh tokens are stored as-is in SQLite: they are unusable without the client secret, which lives only in the config file, so a leaked database alone grants no calendar access.

## Consequences

- hima cannot recognise its own written-back Bookings in free/busy results (unlike CalDAV, where it skips its UIDs). Instead, Calendar sources are re-read after every write, so a moved or cancelled Booking stops blocking within seconds.
- Busy time from these providers ignores `all_day_blocks`; the provider's own "show as" setting decides.
- Bookings are written to at most one calendar across CalDAV, Google and Microsoft.
