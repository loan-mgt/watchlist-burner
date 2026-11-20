# AGENTS.md

## Overview

Single-module Go app (module `watchlist-burner`, Go 1.26). It scrapes a
Letterboxd user's watchlist, matches each film against a Jellyfin library
(normalized title + year, one-year tolerance, unique-title fallback, then TMDB
id when resolvable) and shows only what has not been watched. The frontend is a
plain TypeScript single-page app bundled with esbuild and embedded into the
binary.

## Entrypoint & build

- Entrypoint: `cmd/watchlist-burner/main.go` (the root `main.go` is gone).
- Frontend source in `web/` (`src/`, `index.html`, `public/`, `build.mjs`).
  `web/dist/` is **generated and committed** so `go build` works without Node.
  After changing anything under `web/src` or `web/index.html`, run
  `npm --prefix web run build`; the binary serves `web/dist` via `//go:embed`
  and there is no dev server.
- Run: `go run ./cmd/watchlist-burner` (needs outbound network for Letterboxd
  and Jellyfin).
- Default listen `:8097` (`ADDR` / `-addr`); cache TTL `1h` (`CACHE_TTL` /
  `-cache-ttl`); all other knobs are env-only — see `README.md`.
- `-healthcheck` probes the local `/healthz` and exits (used by Docker).
- Checks: `go vet ./...`, `go test -race ./...`,
  `npm --prefix web run check` (tsc strict), `npm --prefix web run build`.
- `go build ./cmd/watchlist-burner` requires `web/dist` to exist (it is
  committed). If you delete it, run the frontend build first.

## Architecture

- `internal/config` — env/flag loading and validation; fails fast at boot.
- `internal/jellyfin` — REST client plus the SSRF guard.
  - `headers.go`: builds the Emby/Jellyfin auth header value and sets **both**
    `Authorization` and `X-Emby-Authorization`.
  - `safeurl.go`: `Guard` validates user-supplied URLs and dials resolved,
    validated IPs (loopback/RFC1918/CGNAT/link-local blocked unless
    `ALLOW_PRIVATE_JELLYFIN`; link-local always blocked). All Jellyfin traffic
    must use `Guard.HTTPClient`.
  - `client.go`: `Me` (token validation) and `Movies` (paged `/Items` with
    `fields=UserData,ProductionYear,ProviderIds,OriginalTitle` and
    `enableImages=false`, ~44% smaller payload).
- `internal/letterboxd` — `Scraper` (HTML watchlist, paced, retried, capped),
  `MetadataResolver` (film page → `data-tmdb-id` + JSON-LD poster, cached 6h,
  single-flight plus a global pacer).
- `internal/match` — pure matching: name/original name normalization (NFKD
  diacritics, punctuation, leading articles, roman numerals), title+year,
  `±1` year, unique-title fallback, TMDB id.
- `internal/cache` — generic TTL `LRU[K,V]` and a context-aware `Group[K,V]`
  single-flight.
- `internal/burner` — the deep module. Owns the per-user result cache (key =
  `sha256(server|user|lbUser)`), single-flight collapsing, the global scrape
  semaphore, `Authenticate` (URL policy + `/Users/Me`) and `BurnSession`.
- `internal/httpapi` — thin adapters: method-pattern routing, middleware
  (request id, slog access log, panic recovery, security headers, body limit,
  per-IP rate limit), static handler with pre-compressed assets, NDJSON stream.
- `web/` — `embed.go` (`//go:embed all:dist`) and the TypeScript app:
  `jellyfin.ts` (browser → Jellyfin auth), `api.ts` (backend stream client),
  `ui.ts`, `app.ts` (state machine).

## Auth model (do not regress)

- The **browser** authenticates against Jellyfin (Quick Connect, or
  `AuthenticateByName` with the password). The password never reaches this
  backend; there is no `/api/login` and no server-side session.
- The backend receives `X-Jellyfin-URL`, `X-Jellyfin-Token` and
  `X-Jellyfin-User-Id` per request, validates the token with `/Users/Me`, and
  builds the result.
- Tokens stay in `sessionStorage`; never put a token in a URL or query string.

## Behavior gotchas

- Jellyfin 10.9+ requires the standard `Authorization` header; the legacy
  `X-Emby-Authorization`/`X-Emby-Token` headers alone are rejected (`400`/`401`)
  by current servers. Send both (the helpers do).
- Watched films (`UserData.Played`) are hidden; remaining films are sorted
  in-library first, preserving Letterboxd order within each group.
- The library fetch is filtered to the watchlist's distinct years; if any film
  has an unparsed year, the whole library is fetched so title-only matches work.
  More than 200 distinct years also disables the filter (URL length).
- `/api/watchlist/stream` authenticates **before** writing `200` so bad URLs and
  expired tokens keep real status codes; later failures arrive as `error` frames.
- Stream frames: `phase`, `film` (every 100th library item), `ping` (5s
  heartbeat), `result`, `error`.
- Only films that title matching could not resolve trigger a film-page fetch for
  TMDB enrichment, capped by `MAX_ENRICH`.

## Tests

- `internal/jellyfin/client_test.go` asserts the auth headers on every request
  (regression guard), pagination ordering and error mapping.
- `internal/jellyfin/safeurl_test.go` covers the SSRF policy.
- `internal/letterboxd` tests use `testdata/` fixtures and a rewriting
  `RoundTripper`; `internal/match` has normalization/matching tables.
- `internal/burner` uses a fake Jellyfin `httptest` server plus stub
  scraper/metadata seams.
- `internal/httpapi` exercises the full stream against a fake Jellyfin, including
  rate limiting, blocked addresses, static assets and the removed legacy login.

## Docker

- Multi-stage: `node:22-alpine` builds the frontend, `golang:1.26-alpine` builds
  a static binary, `gcr.io/distroless/static-debian12:nonroot` runs it.
- `compose.yaml` runs read-only with `cap_drop: ALL`, `no-new-privileges`, a
  healthcheck and a 256 MB memory limit.
- CI (`.github/workflows/build-publish.yml`) runs vet, race tests, the frontend
  type check/build, verifies `web/dist` is current, then builds/pushes a signed
  multi-arch image with SBOM and provenance.
