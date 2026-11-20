package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"watchlist-burner/internal/burner"
	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
	"watchlist-burner/internal/seerr"
)

// apiError is an error with a stable machine-readable code and HTTP status.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func badRequest(code, message string) *apiError {
	return &apiError{status: http.StatusBadRequest, code: code, message: message}
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// classify maps domain errors to a status and a stable, non-leaking message.
func classify(err error) (status int, code, message string) {
	var apiErr *apiError
	switch {
	case errors.As(err, &apiErr):
		return apiErr.status, apiErr.code, apiErr.message
	case errors.Is(err, burner.ErrUnauthorized):
		return http.StatusUnauthorized, "jellyfin_unauthorized", "Jellyfin rejected the session, please sign in again."
	case errors.Is(err, letterboxd.ErrWatchlistNotFound):
		return http.StatusNotFound, "watchlist_not_found", "No Letterboxd watchlist found for that username."
	case errors.Is(err, seerr.ErrInvalidJellyfinURL):
		return http.StatusBadRequest, "invalid_server_url", "That Jellyfin server URL is not valid."
	case errors.Is(err, jellyfin.ErrBlockedAddress):
		return http.StatusBadRequest, "blocked_address", "That Jellyfin address is not reachable from this server."
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "The request took too long, please retry."
	default:
		return http.StatusBadGateway, "upstream_error", "Could not complete the request, please retry."
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError renders a classified error. Upstream details are logged, never
// echoed, so a public instance does not leak internal addresses or bodies.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if errors.Is(err, context.Canceled) {
		return // client went away
	}
	status, code, message := classify(err)
	if status >= 500 {
		log.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
	}
	writeJSON(w, status, errorBody{Code: code, Message: message})
}
