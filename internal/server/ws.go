package server

import (
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

	Films []WatchlistFilm    `json:"films,omitempty"` // for type=result
	Meta  *watchlistResponse `json:"meta,omitempty"`
	Error string             `json:"error,omitempty"` // for type=error
}

// HandleWatchlistWS streams progress while the watchlist is being built.
// The client connects with ?lbuser=<letterboxd username> and receives a
// "phase" message, one "film" message per scraped entry, then a "result",
// "error", or "auth" message.
func (s *Server) HandleWatchlistWS(ws *websocket.Conn) {
	lbUser := strings.TrimSpace(ws.Request().URL.Query().Get("lbuser"))
	if lbUser == "" {
		_ = websocket.JSON.Send(ws, wsMessage{Type: "error", Error: "missing lbuser query param"})
		return
	}

	sess := s.store.Get()
	if sess == nil {
		_ = websocket.JSON.Send(ws, wsMessage{Type: "auth"})
		return
	}

	// Reuse the same lock as the HTTP path so a build isn't run twice.
	s.refreshSem <- struct{}{}
	defer func() { <-s.refreshSem }()

	if films, meta, ok := s.cache.get(lbUser); ok {
		_ = websocket.JSON.Send(ws, wsMessage{Type: "result", Films: films, Meta: meta})
		return
	}

	send := func(m wsMessage) {
		if err := websocket.JSON.Send(ws, m); err != nil {
			log.Printf("ws send: %v", err)
		}
	}

	send(wsMessage{Type: "phase", Phase: "Checking what you already have in stock…"})

	films, meta, err := s.build(ws.Request().Context(), lbUser, func(it jellyfin.Item) {
		send(wsMessage{Type: "film", Title: it.Name, Year: yearOf(it), Slug: it.ID})
	})
	if err != nil {
		if errors.Is(err, jellyfin.ErrSessionExpired) {
			s.store.Clear()
			send(wsMessage{Type: "auth"})
			return
		}
		log.Printf("watchlist error: %v", err)
		send(wsMessage{Type: "error", Error: err.Error()})
		return
	}

	s.cache.put(lbUser, films, meta)

	send(wsMessage{Type: "result", Films: films, Meta: meta})
}
