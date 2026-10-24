# Owner auth delegated to the reverse proxy, failing closed

The admin is protected primarily by the Owner's reverse proxy (Authelia, Tailscale, Cloudflare Access, …), with a built-in password login as an alternative. Admin and public routes share one port, separated by path. Because a proxy-auth default makes an accidentally exposed open admin easy, `auth.mode` (`proxy` | `password`) is mandatory with no default — the server refuses to start without it — and `proxy` mode binds to localhost unless a listen address is set explicitly.

## Considered Options

- Built-in password only — rejected: duplicates auth most self-hosters already run.
- Separate admin and public ports — deferred: safer by construction, but unnecessary while path separation plus fail-closed config is enough.
