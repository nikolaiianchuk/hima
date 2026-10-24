# Install hima with Caddy

This guide runs hima and [Caddy](https://caddyserver.com) with Docker Compose on a server with a public domain. Caddy gets HTTPS certificates automatically and protects the admin; Guests reach Invite and Manage links without logging in.

You'll end up with:

| Path | Who | Protected by |
|---|---|---|
| `/admin/…` | you | Caddy (or hima's own login, see [Option C](#option-c-hima-password-instead-of-caddy)) |
| `/i/…` Invite links | Guests | the unguessable link |
| `/b/…` Manage links | Guests | the unguessable link |
| `/feed/….ics` Owner feed | your calendar app | the unguessable link |

## Before you start

- A server with Docker and the Compose plugin (Podman with `podman compose` works too).
- A domain pointing at the server, e.g. `hima.example.com` with an `A`/`AAAA` record.
- Ports **80** and **443** open to the internet (Caddy needs both to get certificates).

## 1. Get hima

There is no published image yet, so build it from source:

```sh
mkdir -p /opt/hima && cd /opt/hima
git clone https://github.com/nikolaiianchuk/hima.git src
```

## 2. Configure hima

Create `/opt/hima/hima.yaml` (all options are explained in [`hima.example.yaml`](../hima.example.yaml)):

```yaml
base_url: https://hima.example.com

owner:
  name: Nikolai
  emails: [you@example.com]          # optional: ignore invitations you declined

auth:
  mode: proxy                        # Caddy protects /admin (see step 4)

availability:
  timezone: Europe/Lisbon
  weekly:
    mon: ["10:00-18:00"]
    tue: ["10:00-18:00"]
    wed: ["10:00-18:00"]
    thu: ["10:00-18:00"]
    fri: ["10:00-18:00"]
  buffer: 15m
  min_notice: 12h

calendars:
  - url: https://calendar.google.com/calendar/ical/…/private-…/basic.ics

notifications:
  ntfy:
    url: https://ntfy.sh/pick-a-long-random-topic
```

> The container already listens on `:8080` and stores its database in the `/data` volume, so you don't need `listen` or `database` here.

**Where to find your calendar's private ICS URL**

- Google Calendar: *Settings → your calendar → Integrate calendar → Secret address in iCal format*
- iCloud: *Calendar app → share the calendar → Public Calendar*, then change `webcal://` to `https://`
- Fastmail / Nextcloud: the calendar's *Share* or *Export* settings

## 3. Compose file

Create `/opt/hima/compose.yaml`:

```yaml
services:
  hima:
    build: ./src
    restart: unless-stopped
    volumes:
      - hima-data:/data
      - ./hima.yaml:/etc/hima/hima.yaml:ro
    # No "ports:" on purpose: in proxy mode hima trusts every request that
    # reaches it, so only Caddy may talk to it.

  caddy:
    image: caddy:2
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data
      - caddy-config:/config

volumes:
  hima-data:
  caddy-data:
  caddy-config:
```

## 4. Caddyfile: protect the admin

Pick **one** option.

### Option A: Caddy password prompt (simplest)

Generate a password hash:

```sh
docker compose run --rm caddy caddy hash-password
```

Create `/opt/hima/Caddyfile`, replacing the domain, user name and hash:

```caddy
hima.example.com {
	encode zstd gzip

	@admin path /admin /admin/*
	basic_auth @admin {
		nikolai $2a$14$REPLACE_WITH_THE_HASH
	}

	reverse_proxy hima:8080
}
```

### Option B: single sign-on (Authelia, Authentik, …)

> For a complete walkthrough with Authentik and a self-hosted ntfy, see [install-caddy-authentik-ntfy.md](install-caddy-authentik-ntfy.md).

If you already run an SSO portal, let it guard the admin and pass your user name to hima. With Authelia:

```caddy
hima.example.com {
	encode zstd gzip

	@admin path /admin /admin/*
	forward_auth @admin authelia:9091 {
		uri /api/authz/forward-auth
		copy_headers Remote-User Remote-Groups Remote-Email Remote-Name
	}

	reverse_proxy hima:8080
}
```

Then make hima insist on that header, so a misconfigured proxy fails closed:

```yaml
auth:
  mode: proxy
  proxy_header: Remote-User
```

Put the Authelia container on the same Compose network (or adjust `authelia:9091`). For Authentik, use its `/outpost.goauthentik.io/auth/caddy` endpoint and header `X-Authentik-Username`.

### Option C: hima password instead of Caddy

Let hima show its own login page and keep Caddy as a plain proxy:

```caddy
hima.example.com {
	encode zstd gzip
	reverse_proxy hima:8080
}
```

Generate the hash (you'll be asked for the password):

```sh
docker compose run --rm hima hash-password
```

and put it in `hima.yaml`, **in single quotes** because it contains `$`:

```yaml
auth:
  mode: password
  password_hash: '$2a$10$REPLACE_WITH_THE_HASH'
```

## 5. Start

```sh
cd /opt/hima
docker compose up -d --build
docker compose logs -f
```

You should see `listening addr=:8080 auth=proxy` from hima and certificate messages from Caddy.

## 6. Check it

```sh
curl -s https://hima.example.com/healthz            # → ok
curl -s -o /dev/null -w '%{http_code}\n' https://hima.example.com/admin/   # → 401 (A) / 302 (B) / 303 (C)
```

Then open `https://hima.example.com/admin/` in a browser, log in, and:

1. **Create an invite** and open the link in a private window: you should see your free times without logging in.
2. **Create a feed link** under *Your calendar* and subscribe to it in your calendar app.
3. Book a test slot and check that the notification arrives.

If the admin shows *"Your calendars couldn't be read recently"*, check the ICS URL in `hima.yaml` and `docker compose logs hima`.

## Updating

```sh
cd /opt/hima/src && git pull
cd /opt/hima && docker compose up -d --build hima
```

The database migrates itself on start.

## Backups

Everything lives in one SQLite file in the `hima-data` volume. Stop hima briefly so the copy is consistent:

```sh
cd /opt/hima
docker compose stop hima
docker run --rm -v hima_hima-data:/data -v "$PWD":/backup busybox cp /data/hima.db /backup/hima-$(date +%F).db
docker compose start hima
```

(The volume is named `<project>_hima-data`; `docker volume ls` shows the exact name.)

## Caddy already runs on the host

If Caddy is installed directly on the server rather than in Docker, publish hima on localhost only:

```yaml
services:
  hima:
    build: ./src
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"     # never 0.0.0.0 in proxy mode
    volumes:
      - hima-data:/data
      - ./hima.yaml:/etc/hima/hima.yaml:ro
volumes:
  hima-data:
```

and use `reverse_proxy localhost:8080` in your Caddyfile.

## Troubleshooting

| Symptom | Fix |
|---|---|
| hima exits with `auth.mode is required` | Set `auth.mode` in `hima.yaml`, and check the file is mounted at `/etc/hima/hima.yaml`. |
| Guests see *"Booking is temporarily unavailable"* | A calendar URL has failed for over an hour. Check `docker compose logs hima`. |
| Links in the admin point to the wrong host | Set `base_url` to your public URL. |
| Admin is reachable without a password | You're in proxy mode with no protection in Caddy for `/admin`, or hima's port is published publicly. Fix the Caddyfile or the `ports:` line. |
| `401` on `/admin` even after logging in (Option B) | The proxy isn't sending `proxy_header`. Check `copy_headers`. |
