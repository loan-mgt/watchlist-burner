# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/watchlist-burner .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/watchlist-burner /usr/local/bin/watchlist-burner
EXPOSE 8097
ENTRYPOINT ["/usr/local/bin/watchlist-burner"]
