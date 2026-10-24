# hima

**hima** (暇, "free time") is a self-hosted scheduling tool for you and your friends. You send a friend an expiring Invite link; they pick a time that's free in your calendar and book it. No emails, no accounts for Guests.

## What it does

- **Invites**: expiring links with a use count, allowed durations, an optional date range and a Location. Shown once, revocable, editable (expiry, uses, label).
- **Free/busy from your calendars**: any number of private ICS URLs (Google, iCloud, Fastmail, Nextcloud, Outlook). Recurring events, moved or cancelled occurrences, events marked free, all-day events and invitations you declined are all handled. Guests only ever see free Slots, or optionally busy blocks, never titles.
- **Availability**: weekly hours in your time zone, buffers, minimum notice, booking horizon.
- **Guests** pick a Slot in their own time zone, give their name, get a `.ics` file and a private Manage link to reschedule or cancel.
- **Your calendar**: a private feed of all Bookings to subscribe to.
- **Notifications** via ntfy (optionally with the `.ics` attached) and/or a webhook, plus an activity log.
- **Your look**: pick an accent colour in Settings; it applies to the admin and every Guest page.
- **Small and private**: one Go binary, one SQLite file, fonts bundled (Guest pages make no third-party requests), no outgoing calls except to your calendars and notification endpoints. Old data is deleted after 90 days.

> Early development. See [docs/roadmap.md](docs/roadmap.md) for what's planned and [CONTEXT.md](CONTEXT.md) for the domain language.

## Install

- **[With Docker and Caddy](docs/install-caddy.md)**: recommended; HTTPS, admin protection and backups included.
- **[Full self-hosted stack: Caddy + Authentik + ntfy](docs/install-caddy-authentik-ntfy.md)**: single sign-on for the admin and your own push server.

hima needs `auth.mode` set and refuses to start without it:

- `proxy`: your reverse proxy (Caddy `basic_auth`, Authelia, Tailscale, …) protects `/admin`. hima then trusts every request that reaches it, so never expose its port directly.
- `password`: hima shows its own login page; generate the hash with `hima hash-password`.

See [ADR 0002](docs/adr/0002-owner-auth-delegated-to-proxy.md) for why.

## Run locally

Requires Go 1.26+.

```sh
cp hima.example.yaml hima.yaml   # set your time zone and calendar URL
make run                          # http://127.0.0.1:8080/admin/
```

All options are documented in [`hima.example.yaml`](hima.example.yaml). Every option can also be set with a `HIMA_*` environment variable (e.g. `HIMA_AUTH_MODE`, `HIMA_NTFY_URL`), which overrides the file.

## Develop

`templ` is pinned as a module tool, so no global install is needed.

```sh
make test    # templ generate + go test
make build   # bin/hima
podman build --format docker -t hima .   # or: docker build -t hima .
```

Generated `*_templ.go` files are committed so `go build` works without running `templ`.

## Docs

- [Install with Caddy](docs/install-caddy.md)
- [Self-host with Caddy, Authentik and ntfy](docs/install-caddy-authentik-ntfy.md)
- [CONTEXT.md](CONTEXT.md): glossary
- [docs/adr](docs/adr): architecture decisions
- [docs/roadmap.md](docs/roadmap.md): MVP and later phases
