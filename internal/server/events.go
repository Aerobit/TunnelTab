package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Aerobit/TunnelTab/internal/sshx"
)

// Events sent to the dashboard over GET /api/events (Server-Sent Events
// format, read with fetch so the session header can be sent).
type (
	tunnelEvent struct {
		Type string `json:"type"` // "tunnel"
		sshx.Event
	}
	vaultEvent struct {
		Type  string `json:"type"`  // "vault"
		State string `json:"state"` // "locked", "unlocked"
	}
	dataEvent struct {
		Type string `json:"type"` // "data": something changed; re-fetch /api/data
	}
)

const heartbeatInterval = 20 * time.Second

// broker fans events out to every connected dashboard. Slow subscribers
// miss events rather than blocking the app; they receive a "resync" event so
// they can re-fetch the full state.
type broker struct {
	mu     sync.Mutex
	subs   map[chan []byte]bool
	closed bool
}

func newBroker() *broker { return &broker{subs: map[chan []byte]bool{}} }

func (b *broker) publish(ev any) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Only publish writes to subscriber channels, and it holds b.mu, so the
	// length check can't race with another writer. One slot is always kept
	// free so the resync notice is guaranteed to fit.
	for ch, ok := range b.subs {
		if !ok {
			continue // waiting for the subscriber to read its resync notice
		}
		if len(ch) < cap(ch)-1 {
			ch <- data
		} else {
			b.subs[ch] = false
			ch <- []byte(`{"type":"resync"}`)
		}
	}
}

func (b *broker) subscribe() chan []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan []byte, 64)
	if b.closed {
		close(ch)
		return ch
	}
	b.subs[ch] = true
	return ch
}

func (b *broker) unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
}

// resume re-enables delivery to a subscriber after it has resynced.
func (b *broker) resume(ch chan []byte) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		b.subs[ch] = true
	}
	b.mu.Unlock()
}

func (b *broker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for ch := range b.subs {
		close(ch)
	}
	b.subs = map[chan []byte]bool{}
}

// handleEvents streams events until the client disconnects.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// A terminal page passes its session ID: while this stream is open, the
	// session is kept even when no page is attached (e.g. while locked).
	if id := r.URL.Query().Get("terminal"); id != "" {
		defer s.watchTerminal(id)()
	}
	// The dashboard passes its per-tab ID: terminals it shows are kept while
	// this stream is open.
	if id := r.URL.Query().Get("client"); validClientID.MatchString(id) {
		defer s.watchClient(id)()
	}
	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)
	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			if string(data) == `{"type":"resync"}` {
				s.events.resume(ch)
			}
		case <-hb.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// count reports how many event streams (dashboards and terminal pages) are open.
func (b *broker) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
