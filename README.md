# hima

**hima** (暇, "free time") is a self-hosted scheduling tool for you and your friends. Share an expiring Invite link; friends book time against your free/busy calendar. No emails, no Guest accounts.

> Early development. See [docs/roadmap.md](docs/roadmap.md) for what's planned and [CONTEXT.md](CONTEXT.md) for the domain language.

## Run

```sh
cp hima.example.yaml hima.yaml   # set auth.mode
make run
```

Or with Docker (proxy mode, so the admin must sit behind your reverse proxy):

```sh
docker build -t hima .
docker run -p 8080:8080 -v hima-data:/data \
  -e HIMA_AUTH_MODE=proxy -e HIMA_LISTEN=:8080 hima
```

For the built-in login instead, generate a hash and use password mode:

```sh
go run ./cmd/hima hash-password
# then set auth.mode: password and auth.password_hash
```

## Develop

Requires Go 1.26+. `templ` is pinned as a module tool, so no global install is needed.

```sh
make test    # templ generate + go test
make build   # bin/hima
```

Generated `*_templ.go` files are committed so `go build` works without running `templ`.

## Docs

- [CONTEXT.md](CONTEXT.md) — glossary
- [docs/adr](docs/adr) — architecture decisions
- [docs/roadmap.md](docs/roadmap.md) — MVP and later phases
