# AGENTS.md

## Overview
Single-module Go app (module `watchlist-burner`, go 1.26). Scrapes a Letterboxd user's watchlist, matches each film against a Jellyfin library by normalized title + year, and shows only what hasn't been watched.

## Entrypoint & build
- The binary entrypoint is the root `main.go`, **not** `cmd/` — `cmd/` is an empty directory, ignore it.
- Frontend lives in `web/` and is embedded with `//go:embed web/templates web/static`. There is **no dev server and no hot reload**: after editing anything under `web/` you must restart the process (`go run .`) for changes to appear.
- Run: `go run .` — needs outbound network access to `letterboxd.com` plus reachable Jellyfin at runtime.
- Default listen address `:8097`; override with `ADDR` env or `-addr` flag. Watchlist cache lifetime `1h` via `CACHE_TTL` env or `-cache-ttl` flag.
- There are no tests. `go vet ./...` is the main static sanity check; `go build .` for the binary.

## Architecture
- `internal/jellyfin/client.go` — Jellyfin REST client (`/Users/AuthenticateByName`, paged `/Items`). `SessionStore` holds exactly **one in-memory Jellyfin session**; single-user, no persistence, cleared on logout or expired-session error (`ErrSessionExpired`).
- `internal/letterboxd/scraper.go` — HTML scraper (no official API) using `golang.org/x/net/html`; walks watchlist pages and resolves posters via the LTRBXD CDN, with HEAD checks + film-page JSON-LD fallback.
- `internal/server/` — HTTP + WebSocket handlers. `handlers.go` holds the shared cache (keyed only by Letterboxd username, TTL-based); `ws.go` streams per-movie progress over `golang.org/x/net/websocket` (the deprecated package, not gorilla/nhooyr); `matcher.go` matches films to library items (lowercased, leading-article-stripped, punctuation-dropped title + year).

## Behavior gotchas
- Watched films (Jellyfin `UserData.Played`) are **hidden** from results; remaining films are sorted in-library first, Letterboxd order preserved within each group.
- Login flow: browser POSTs Jellyfin password to the backend, which opens a short-lived session via `AuthenticateByName`. Credentials themselves are not stored server-side.
- Matches a single Jellyfin "Movies" library (TV series etc. excluded).

## Docker
- `Dockerfile` builds statically (`CGO_ENABLED=0`) into a distroless nonroot image, entrypoint at port 8097.
- `compose.yaml` publishes `8097:8097` and sets `CACHE_TTL=1h`; per environment preferences use podman/podman-compose.
