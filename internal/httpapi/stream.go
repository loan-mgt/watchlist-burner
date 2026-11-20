package httpapi

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"watchlist-burner/internal/burner"
)

// frame is one NDJSON message streamed to the browser.
type frame struct {
	Type string `json:"type"` // phase | film | result | error | ping

	Phase string `json:"phase,omitempty"`

	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
	Index int    `json:"index,omitempty"`
	Total int    `json:"total,omitempty"`

	Films []burner.Film `json:"films,omitempty"`
	Meta  *resultMeta   `json:"meta,omitempty"`

	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type resultMeta struct {
	Total   int `json:"total"`
	Matched int `json:"matched"`
	Watched int `json:"watched"`
}

// streamWriter serializes frames from the burn goroutine and the heartbeat.
type streamWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
	enc     *json.Encoder
}

func newStreamWriter(w http.ResponseWriter, f http.Flusher) *streamWriter {
	return &streamWriter{w: w, flusher: f, enc: json.NewEncoder(w)}
}

func (s *streamWriter) send(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enc.Encode(v); err != nil {
		return
	}
	s.flusher.Flush()
}

// heartbeat keeps proxies and the browser's idle timer from giving up while a
// long library fetch produces no film frames.
func (s *streamWriter) heartbeat(every time.Duration) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				s.send(frame{Type: "ping"})
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}
