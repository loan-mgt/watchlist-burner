# Watchlist Burner

Cross your [Letterboxd](https://letterboxd.com) watchlist with your Jellyfin
library and see what is actually worth watching tonight: in-library films first,
already-watched films hidden, everything else dimmed.

```
Browser ──(password or Quick Connect)──▶ Jellyfin        auth + token
   │                                        ▲
   │  token in request headers only         │ /Users/Me + /Items
   ▼                                        │
watchlist-burner (stateless) ───────────────┘
   │  • no session store, no password, no token at rest
   └─▶ Letterboxd watchlist scrape (server-side, paced)
```

## Why the auth flow looks like this

Your Jellyfin password **never reaches this server**. The browser talks to your
Jellyfin directly (over CORS) using either **Quick Connect** (approve a 6-digit
code in any Jellyfin client, no password typed anywhere) or the classic password
form. The resulting access token is kept in `sessionStorage` and sent to this
backend in request headers for each burn. The backend validates it against
`/Users/Me` and uses it to read your library.

Jellyfin 10.9+ uses the standard `Authorization: MediaBrowser …` header. Older
servers only understand `X-Emby-Authorization`, so both are sent.

## Quick start

```sh
podman-compose up -d --build   # or docker compose
# open http://localhost:8097
```

Or run the binary directly:

```sh
npm --prefix web install
npm --prefix web run build     # assets are embedded in the binary
go run ./cmd/watchlist-burner
```

`web/dist` is committed, so `go build ./cmd/watchlist-burner` works without a
Node toolchain.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `ADDR` | `:8097` | Listen address (`-addr` flag also works) |
| `CACHE_TTL` | `1h` | Watchlist result cache lifetime (`-cache-ttl`) |
| `CACHE_SIZE` | `500` | Max cached per-user results (LRU) |
| `BURN_BUDGET` | `60s` | Max time for one burn |
| `SCRAPE_CONCURRENCY` | `8` | Max concurrent burns hitting Letterboxd |
| `MAX_FILMS` | `5000` | Watchlist size cap |
| `MAX_WATCHLIST_PAGES` | `50` | Watchlist page cap |
| `MAX_ENRICH` | `25` | Max film pages fetched to resolve TMDB ids |
| `RATE_LIMIT_PER_MINUTE` | `30` | Per-IP token bucket (`0` disables) |
| `MAX_BODY_BYTES` | `8192` | Request body cap |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` or `text` |
| `ALLOW_PRIVATE_JELLYFIN` | `false` | Allow RFC1918/loopback Jellyfin targets |
| `ALLOW_INSECURE_JELLYFIN` | `false` | Allow `http://` and non-standard ports |
| `ALLOWED_JELLYFIN_HOSTS` | – | Comma-separated host allowlist |
| `TRUST_PROXY_HEADERS` | `true` | Honour `X-Forwarded-For` / `X-Forwarded-Proto` |

Self-hosting Jellyfin on your LAN behind this app requires
`ALLOW_PRIVATE_JELLYFIN=true` and usually `ALLOW_INSECURE_JELLYFIN=true`.

## API

| Method | Path | Notes |
|---|---|---|
| `GET` | `/` | App shell |
| `GET` | `/api/config` | Public client config |
| `POST` | `/api/watchlist/stream?lbuser=` | NDJSON: `phase`, `film`, `ping`, `result`, `error` |
| `POST` | `/api/watchlist?lbuser=` | Same input, single JSON response |
| `GET` | `/api/poster?slug=` | Letterboxd poster fallback (cached) |
| `GET` | `/healthz`, `/readyz` | Liveness / readiness |
| `GET` | `/assets/*` | Hashed assets, immutable, pre-compressed |

Session headers for the two `watchlist` endpoints:

```
X-Jellyfin-URL: https://jellyfin.example.com
X-Jellyfin-Token: <access token>
X-Jellyfin-User-Id: <user id>
```

## Security posture (public deployments)

- **Passwords never hit the server.** Quick Connect avoids passwords entirely.
- **No cookies, tokens in headers** → no CSRF surface.
- **SSRF guard**: HTTPS-only by default, port allowlist, DNS resolved and
  validated inside the dialer (blocks private, loopback, CGNAT, link-local and
  cloud-metadata addresses), redirects re-validated. Operator can opt into LAN
  targets or an explicit host allowlist.
- **Bounded work**: per-IP rate limits, watchlist/library/page caps, a global
  scrape concurrency limit, per-request budgets and a size-capped LRU cache keyed
  by `sha256(server | user | letterboxd user)`.
- **CSP without inline script/style**, `nosniff`, `no-referrer`,
  `frame-ancestors 'none'`, HSTS behind TLS.
- Upstream error bodies are logged, never echoed to clients.
- Tokens are never put in URLs or query strings, so they cannot leak through
  logs or `Referer`.

Run it behind a TLS-terminating reverse proxy (Caddy, nginx, Traefik) and keep
`TRUST_PROXY_HEADERS=true` only when that proxy overwrites the forwarding
headers.

## Development

```sh
go test -race ./...            # backend unit + integration tests
go vet ./...
npm --prefix web run check     # TypeScript strict check
npm --prefix web run build     # rebuild embedded assets
```

The frontend is plain TypeScript bundled with esbuild: no framework, no runtime
dependencies, content-hashed assets, pre-compressed with Brotli and gzip at
build time (~9 KB over the wire).
