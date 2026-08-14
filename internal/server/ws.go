package server

import (
	"context"
	"errors"
	"log"
	"strings"

	"golang.org/x/net/websocket"

	"watchlist-burner/internal/jellyfin"
)

// wsMessage is a single frame sent to the browser over the watchlist socket.
type wsMessage struct {
	Type  string `json:"type"`            // "phase" | "film" | "result" | "error" | "auth"
	Phase string `json:"phase,omitempty"` // for type=phase
	Title string `json:"title,omitempty"` // for type=film
	Year  int    `json:"year,omitempty"`
	Slug  string `json:"slug,omitempty"`
	Index int    `json:"index,omitempty"` // for type=film: 0-based position
	Total int    `json:"total,omitempty"` // for type=film: total record count

	Films []WatchlistFilm    `json:"films,omitempty"` // for type=result
	Meta  *watchlistResponse `json:"meta,omitempty"`
	Error string             `json:"error,omitempty"` // for type=error
}

func sendWS(ws *websocket.Conn, m wsMessage) {
	if err := websocket.JSON.Send(ws, m); err != nil {
		log.Printf("ws send: %v", err)
	}
}

// HandleWatchlistWS streams progress while the watchlist is being built.
// The client connects with ?lbuser=<letterboxd username> and receives a
// "phase" message, one "film" message per scraped entry, then a "result",
// "error", or "auth" message.
func (s *Server) HandleWatchlistWS(ws *websocket.Conn) {
	lbUser := strings.TrimSpace(ws.Request().URL.Query().Get("lbuser"))
	if lbUser == "" {
		sendWS(ws, wsMessage{Type: "error", Error: "missing lbuser query param"})
		return
	}

	// Cancel the build as soon as the client disconnects so the refresh
	// semaphore is released promptly instead of being held until the whole
	// build finishes. The browser never sends frames after connecting, so
	// Read only returns when the connection is closed.
	ctx, cancel := context.WithCancel(ws.Request().Context())
	defer cancel()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := ws.Read(buf); err != nil {
				cancel()
				return
			}
		}
	}()

	sendWS(ws, wsMessage{Type: "phase", Phase: "Checking what you already have in stock…"})

	// Stream only every 100th movie to the browser; the frontend derives the
	// exact count and progress from the index/total carried in each frame.
	resp, err := s.burner.Burn(ctx, lbUser, func(index, total int, it jellyfin.Item) {
		if index%100 == 0 {
			sendWS(ws, wsMessage{Type: "film", Title: it.Name, Year: yearOf(it), Slug: it.ID, Index: index, Total: total})
		}
	})
	if err != nil {
		if errors.Is(err, jellyfin.ErrSessionExpired) {
			sendWS(ws, wsMessage{Type: "auth"})
			return
		}
		log.Printf("watchlist error: %v", err)
		sendWS(ws, wsMessage{Type: "error", Error: err.Error()})
		return
	}

	sendWS(ws, wsMessage{Type: "result", Films: resp.Films, Meta: resp})
}
