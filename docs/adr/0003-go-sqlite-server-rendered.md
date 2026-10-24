# Go single binary, SQLite only, server-rendered UI

hima is one Go binary storing everything in SQLite (pure-Go `modernc.org/sqlite`, no CGO) and rendering HTML on the server with `templ` and htmx. A single Owner never produces enough data to justify Postgres, and one file makes backup trivial; Postgres support is deliberately out of scope. The UI is server-rendered with no SPA or Node build so the Guest page loads fast from a chat link and the project stays one toolchain. `templ` was chosen over `html/template` for compile-time type safety.
