# Self-host hima with Caddy, Authentik and ntfy

This guide sets up a complete self-hosted stack on one server with Docker Compose:

| Host | What | Who logs in |
|---|---|---|
| `auth.example.com` | [Authentik](https://goauthentik.io): single sign-on | you |
| `ntfy.example.com` | [ntfy](https://ntfy.sh): push notifications to your phone | your phone app and hima, with their own credentials |
| `hima.example.com` | hima | you (through Authentik) for `/admin`; Guests need nothing |

[Caddy](https://caddyserver.com) sits in front of all three and gets HTTPS certificates automatically.

```
                 ┌──────────────── server ────────────────────────────────┐
 internet ──443──► Caddy ─┬─ auth.example.com ──► authentik-server:9000   │
                 │        ├─ ntfy.example.com ──► ntfy:80                 │
                 │        └─ hima.example.com ─┬─ /admin ─► forward_auth  │
                 │                             │            ▼             │
                 │                             └──────────► hima:8080     │
                 │                                hima ──► ntfy (push)    │
                 └────────────────────────────────────────────────────────┘
```

If you'd rather not run a login portal, [docs/install-caddy.md](install-caddy.md) protects the admin with a Caddy password instead.

## Before you start

- A Linux server with Docker and the Compose plugin, at least 2 GB of RAM (Authentik needs most of it).
- Three DNS records pointing at the server: `auth.example.com`, `ntfy.example.com`, `hima.example.com`. Replace `example.com` throughout this guide.
- Ports **80** and **443** open.

Directory layout we'll create:

```
/opt/authentik/   Authentik (its official Compose file)
/opt/hima/        Caddy, hima and ntfy
```

## 1. Shared network

Caddy and Authentik live in separate Compose projects; this network lets Caddy reach Authentik:

```sh
docker network create proxy
```

## 2. Authentik

Follow Authentik's own install, which keeps upgrades simple:

```sh
mkdir -p /opt/authentik && cd /opt/authentik
wget https://goauthentik.io/docker-compose.yml

echo "PG_PASS=$(openssl rand -base64 36 | tr -d '\n')" >> .env
echo "AUTHENTIK_SECRET_KEY=$(openssl rand -base64 60 | tr -d '\n')" >> .env
# Only Caddy should reach Authentik, so bind its ports to localhost:
echo "COMPOSE_PORT_HTTP=127.0.0.1:9000" >> .env
echo "COMPOSE_PORT_HTTPS=127.0.0.1:9443" >> .env
```

Attach Authentik's web server to the `proxy` network under a stable name. Create `/opt/authentik/docker-compose.override.yml`:

```yaml
services:
  server:
    networks:
      default:
      proxy:
        aliases: [authentik-server]

networks:
  proxy:
    external: true
```

Start it:

```sh
docker compose up -d
```

## 3. hima, ntfy and Caddy

```sh
mkdir -p /opt/hima/ntfy && cd /opt/hima
git clone https://github.com/nikolaiianchuk/hima.git src
```

### ntfy configuration

Create `/opt/hima/ntfy/server.yml`:

```yaml
base-url: https://ntfy.example.com
listen-http: ":80"
behind-proxy: true

# Persist messages for a day so phones that were offline still get them.
cache-file: /var/cache/ntfy/cache.db
cache-duration: 24h

# Private by default: every topic needs a user or token.
auth-file: /var/lib/ntfy/user.db
auth-default-access: deny-all

# Needed for hima's attach_ics (the .ics file in the push).
attachment-cache-dir: /var/cache/ntfy/attachments
attachment-file-size-limit: 1M
attachment-expiry-duration: 24h

# iPhones only get instant pushes through ntfy.sh's relay. It forwards a
# wake-up ping, never the message itself. Remove if you only use Android.
upstream-base-url: https://ntfy.sh
```

### hima configuration

Create `/opt/hima/hima.yaml` (all options: [`hima.example.yaml`](../hima.example.yaml)):

```yaml
base_url: https://hima.example.com

owner:
  name: Nikolai
  emails: [you@example.com]          # optional: ignore invitations you declined

auth:
  mode: proxy
  # Authentik's forward auth sends your user name in this header. hima
  # refuses admin requests without it, so a misconfigured proxy fails closed.
  proxy_header: X-Authentik-Username

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
    # hima talks to ntfy over the internal network.
    url: http://ntfy/hima
    token: ""                        # filled in at step 6
    attach_ics: true
```

Where to find your calendar's private ICS URL:

- Google Calendar: *Settings → your calendar → Integrate calendar → Secret address in iCal format*
- iCloud: *share the calendar → Public Calendar*, then change `webcal://` to `https://`
- Fastmail / Nextcloud: the calendar's *Share* or *Export* settings

### Caddyfile

Create `/opt/hima/Caddyfile`:

```caddy
auth.example.com {
	reverse_proxy authentik-server:9000
}

ntfy.example.com {
	reverse_proxy ntfy:80
}

hima.example.com {
	encode zstd gzip

	# Authentik's login callback and sign-out live under this path.
	handle /outpost.goauthentik.io/* {
		reverse_proxy authentik-server:9000
	}

	# The admin requires an Authentik login.
	@admin path /admin /admin/*
	handle @admin {
		route {
			# Drop any user name a client tries to send itself.
			request_header -X-Authentik-Username
			forward_auth authentik-server:9000 {
				uri /outpost.goauthentik.io/auth/caddy
				copy_headers X-Authentik-Username X-Authentik-Groups X-Authentik-Email X-Authentik-Name X-Authentik-Uid
				trusted_proxies private_ranges
			}
			reverse_proxy hima:8080
		}
	}

	# Invite links, Manage links and the Owner feed are public.
	handle {
		reverse_proxy hima:8080
	}
}
```

### Compose file

Create `/opt/hima/compose.yaml`:

```yaml
services:
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
    networks: [default, proxy]

  hima:
    build: ./src
    restart: unless-stopped
    volumes:
      - hima-data:/data
      - ./hima.yaml:/etc/hima/hima.yaml:ro
    # No "ports:": in proxy mode hima trusts whatever reaches it, so only
    # Caddy may talk to it.

  ntfy:
    image: binwiederhier/ntfy
    command: serve
    restart: unless-stopped
    volumes:
      - ./ntfy/server.yml:/etc/ntfy/server.yml:ro
      - ntfy-cache:/var/cache/ntfy
      - ntfy-lib:/var/lib/ntfy

volumes:
  caddy-data:
  caddy-config:
  hima-data:
  ntfy-cache:
  ntfy-lib:

networks:
  proxy:
    external: true
```

Start everything:

```sh
cd /opt/hima
docker compose up -d --build
docker compose logs -f caddy      # wait for the three certificates
```

## 4. Set up Authentik

1. Open `https://auth.example.com/if/flow/initial-setup/` and set the password for the `akadmin` user.
2. *Optional but recommended:* create your own user under *Directory → Users* and use it instead of `akadmin`.
3. Create the application. Go to *Applications → Applications → Create with provider*:
   - **Application**: name `hima`, slug `hima`.
   - **Provider type**: *Proxy Provider*.
   - **Mode**: *Forward auth (single application)*.
   - **External host**: `https://hima.example.com`
   - Leave the rest as default and finish.
4. **Only let yourself in.** Open the `hima` application → *Policy / Group / User Bindings* → *Bind existing user* → pick your user. Without a binding, every Authentik user could open your admin.
5. Attach it to the outpost. Go to *Applications → Outposts* → edit *authentik Embedded Outpost* → add `hima` to *Applications* → *Update*.

## 5. Check the admin login

Open `https://hima.example.com/admin/` in a private window. You should be sent to Authentik, log in, and land on hima's admin saying *Hi, Nikolai*.

```sh
curl -s https://hima.example.com/healthz                                    # → ok
curl -s -o /dev/null -w '%{http_code}\n' https://hima.example.com/admin/    # → 302 (to Authentik)
curl -s -o /dev/null -w '%{http_code}\n' https://hima.example.com/i/nope    # → 404 (public, no login)
```

To sign out of the admin: `https://hima.example.com/outpost.goauthentik.io/sign_out`.

## 6. Set up ntfy

Create your own user (for the phone) and a publish-only user and token for hima:

```sh
cd /opt/hima
docker compose exec ntfy ntfy user add --role=admin nikolai     # asks for a password
docker compose exec ntfy ntfy user add hima                     # any long random password
docker compose exec ntfy ntfy access hima hima write-only       # may only publish to topic "hima"
docker compose exec ntfy ntfy token add hima                    # prints tk_…
```

Put the token into `/opt/hima/hima.yaml`:

```yaml
notifications:
  ntfy:
    url: http://ntfy/hima
    token: tk_…
    attach_ics: true
```

and restart hima:

```sh
docker compose restart hima
```

Send a test push to check the credentials (should print a JSON message):

```sh
docker compose exec ntfy sh -c 'wget -qO- --header "Authorization: Bearer tk_…" --post-data "hello from the server" http://localhost/hima'
```

### On your phone

Install the ntfy app ([Android](https://play.google.com/store/apps/details?id=io.heckel.ntfy) / [iOS](https://apps.apple.com/app/ntfy/id1625396347)), then:

1. *Settings → Manage users → Add user*: server `https://ntfy.example.com`, user `nikolai` and your password.
2. *Subscribe to topic*: topic `hima`, *use another server* → `https://ntfy.example.com`.

You should see the test message from above if it's still cached.

> **Using the public ntfy.sh instead?** Skip the `ntfy` service, its Caddy block and step 6. Set `url: https://ntfy.sh/<a-long-random-topic>` and leave `token` empty. Anyone who knows the topic name can read it, so make it unguessable.

## 7. First run

In `https://hima.example.com/admin/`:

1. Under **Your calendar**, *Create feed link* and subscribe to it in your calendar app.
2. **Create an invite**, open it in a private window, and book a slot. Your phone should get *New booking: …* with `booking.ics` attached; tapping it adds the event to your calendar.
3. Check the **Activity** list in the admin.

## Updating

```sh
# hima
cd /opt/hima/src && git pull && cd .. && docker compose up -d --build hima
# Caddy and ntfy
cd /opt/hima && docker compose pull caddy ntfy && docker compose up -d
# Authentik: follow its release notes, then
cd /opt/authentik && docker compose pull && docker compose up -d
```

## Backups

| What | Where |
|---|---|
| hima (Invites, Bookings, settings) | `hima.db` in the `hima_hima-data` volume |
| ntfy users and tokens | `user.db` in the `hima_ntfy-lib` volume |
| Authentik | its PostgreSQL database; see Authentik's backup docs |
| Config | `/opt/hima/*.yaml`, `/opt/hima/Caddyfile`, `/opt/hima/ntfy/`, `/opt/authentik/.env` |

hima and ntfy use SQLite, so stop them briefly for a consistent copy:

```sh
cd /opt/hima && docker compose stop hima ntfy
docker run --rm -v hima_hima-data:/hima -v hima_ntfy-lib:/ntfy -v "$PWD":/backup busybox \
  sh -c 'cp /hima/hima.db /backup/hima-$(date +%F).db && cp /ntfy/user.db /backup/ntfy-users-$(date +%F).db'
docker compose start hima ntfy
```

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `/admin` shows hima's *unauthorized* (401) after logging in | Caddy isn't passing `X-Authentik-Username`. Check the `copy_headers` line and that `proxy_header` in `hima.yaml` matches exactly. |
| `/admin` loops back to the Authentik login | The `/outpost.goauthentik.io/*` block is missing, or the application isn't added to the embedded outpost (step 4.5). |
| Authentik says you don't have access | Bind your user to the `hima` application (step 4.4). |
| Caddy logs `dial tcp: lookup authentik-server` | Authentik isn't on the `proxy` network. Check `docker-compose.override.yml` and restart Authentik. |
| Pushes arrive but without `booking.ics` | ntfy has no `attachment-cache-dir`. hima's log then says *ntfy refused the attachment* with ntfy's reason. |
| No pushes at all | Wrong token or topic: hima's log shows `notification failed` with the HTTP error. Re-run the test push in step 6. |
| iPhone gets pushes only when the app is open | `upstream-base-url: https://ntfy.sh` is missing from `server.yml`. |
| Guests see *"Booking is temporarily unavailable"* | A calendar URL has failed for over an hour. See `docker compose logs hima`. |
