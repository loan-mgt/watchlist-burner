# AGENTS.md

## Overview
Single-module Go app (module `watchlist-burner`, go 1.26). Scrapes a Letterboxd user's watchlist, matches each film against a Jellyfin library by normalized title + year, and shows only what hasn't been watched.

## Entrypoint & build
- The binary entrypoint is the root `main.go`, **not** `cmd/` — `cmd/` is an empty directory, ignore it.
- Frontend lives in `web/` and is embedded with `//go:embed web/templates web/static`. There is **no dev server and no hot reload**: after editing anything under `web/` you must restart the process (`go run .`) for changes to appear.
- Run: `go run .` — needs outbound network access to `letterboxd.com` plus reachable Jellyfin at runtime.
- Default listen address `:8097`; override with `ADDR` env or `-addr` flag. Watchlist cache lifetime `1h` via `CACHE_TTL` env or `-cache-ttl` flag.
- There are no integration tests against live servers; `internal/server` has unit tests using in-memory fakes (`go test ./...`). `go vet ./...` is the main static sanity check; `go build .` for the binary.

## Architecture
- `internal/jellyfin/client.go` — Jellyfin REST client (`/Users/AuthenticateByName`, paged `/Items`). `Session` holds an authenticated, ephemeral session; the `SessionStore` wrapper is gone — session ownership moved into the server's `Burner`.
- `internal/letterboxd/scraper.go` — HTML scraper (no official API) using `golang.org/x/net/html`; walks watchlist pages and derives posters from the constructed LTRBXD CDN URL where possible.
- `internal/letterboxd/poster.go` — `PosterResolver`: resolves a film's poster from the film page's JSON-LD image (lazy fallback for missing/invalid constructed URLs). Exposed via `GET /api/poster?slug=…`; the frontend calls it on demand.
- `internal/server/` — HTTP + WebSocket adapters over the core `Burner` module. `burner.go` is the deep module: it owns the single Jellyfin session (single-user, no persistence, cleared on logout or `ErrSessionExpired`), the TTL cache (keyed only by Letterboxd username), the refresh semaphore, matching, and the year-filter decision — behind `Login/Logout/Burn(ctx, lbUser, progress)`. `Library` and `WatchlistSource` are seams (interfaces) the Burner consumes; `JellyfinLibrary` adapts the REST client to `Library`; tests use in-memory fakes. `handlers.go` and `ws.go` are thin adapters over the Burner; `ws.go` streams per-movie progress over `golang.org/x/net/websocket` (the deprecated package, not gorilla/nhooyr); `matcher.go` matches films to library items (lowercased, leading-article-stripped, punctuation-dropped title + year, title-only fallback when a film's year is unknown).

## Behavior gotchas
- Watched films (Jellyfin `UserData.Played`) are **hidden** from results; remaining films are sorted in-library first, Letterboxd order preserved within each group.
- The library fetch is filtered to the watchlist's distinct years as an optimization; if any film has an unparsed year, the whole library is fetched so title-only matches still work.
- Login flow: browser POSTs Jellyfin password to the backend, which opens a short-lived session via `AuthenticateByName`. Credentials themselves are not stored server-side.
- Matches a single Jellyfin "Movies" library (TV series etc. excluded).

## Docker
- `Dockerfile` builds statically (`CGO_ENABLED=0`) into a distroless nonroot image, entrypoint at port 8097.
- `compose.yaml` publishes `8097:8097` and sets `CACHE_TTL=1h`; per environment preferences use podman/podman-compose.
