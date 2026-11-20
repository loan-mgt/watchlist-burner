# syntax=docker/dockerfile:1

# ---- Frontend -----------------------------------------------------------
# esbuild + tsc only: no framework, no bundler runtime. The build emits
# content-hashed assets, a rewritten index.html and pre-compressed variants.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Backend ------------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/watchlist-burner ./cmd/watchlist-burner

# ---- Runtime ------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/watchlist-burner /usr/local/bin/watchlist-burner
EXPOSE 8097
# Distroless has no shell or curl, so the binary probes its own /healthz.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/watchlist-burner", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/watchlist-burner"]
