# Security Policy

## Reporting a vulnerability

Open a private security advisory on GitHub, or email the maintainer. Please do
not file a public issue for exploitable problems.

## Threat model

This app is meant to be deployed publicly for multiple users, each pointing at
their own Jellyfin server. The assets worth protecting are the user's Jellyfin
credentials and the host network the app runs on.

- **Passwords** are entered in the browser and sent directly to Jellyfin. They
  are never transmitted to this server, logged, or stored.
- **Access tokens** live in the browser's `sessionStorage`, travel to this
  backend in request headers only, are never written to disk, and are not put in
  URLs (so they cannot leak via logs or `Referer`).
- **Server-side request forgery** is the main structural risk: the Jellyfin URL
  is user supplied. The guard rejects non-HTTP(S) schemes, URL credentials,
  non-allowlisted ports, and resolves + validates every address inside the
  dialer (blocking loopback, RFC1918, CGNAT, link-local/metadata and multicast)
  unless the operator explicitly opts into private targets. Redirects are
  re-validated and capped.
- **Resource exhaustion** is bounded by per-IP rate limits, request body caps,
  watchlist/library/page limits, a global scrape-concurrency limit and
  per-request deadlines.
- **Client-side injection** is contained by a strict CSP (no inline script or
  style), `nosniff`, `frame-ancestors 'none'` and by rendering all film data with
  DOM APIs rather than HTML strings.

## Supported versions

Only the latest published image is supported.
