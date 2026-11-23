package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RoyalNoob/quest-map/internal/index"
)

// Event is one live update sent to web UI clients over /api/events.
type Event struct {
	Kind    string `json:"kind"`              // task, activity, project, releases, suites, all
	Project string `json:"project,omitempty"` // project slug
	ID      string `json:"id,omitempty"`      // task id, for task events; plugin name, for storage events
	Key     string `json:"key,omitempty"`     // storage events: the key that changed, so a view reloads only it
}

// hub fans index changes out to the open event streams.
type hub struct {
	mu     sync.Mutex
	subs   map[chan Event]bool
	closed bool
}

func newHub() *hub { return &hub{subs: map[chan Event]bool{}} }

// publish sends an event to every subscriber without waiting; a subscriber that has fallen behind gets an
// "all" event instead, which makes it reload everything.
func (h *hub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- Event{Kind: "all"}:
			default:
			}
		}
	}
}

func (h *hub) subscribe() (chan Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	ch := make(chan Event, 64)
	h.subs[ch] = true
	return ch, true
}

func (h *hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[ch] {
		delete(h.subs, ch)
		close(ch)
	}
}

// close ends every stream (on server shutdown).
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// eventFromChange turns an index change into a UI event.
func eventFromChange(c index.Change) Event {
	e := Event{Kind: c.Kind, Project: c.Project}
	if c.Kind == "task" && c.Path != "" {
		e.ID = strings.TrimSuffix(filepath.Base(c.Path), ".md")
	}
	return e
}

// serveEvents streams events as Server-Sent Events until the client leaves or the server stops.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	ch, ok := s.hub.subscribe()
	if !ok {
		http.Error(w, "server is stopping", http.StatusServiceUnavailable)
		return
	}
	defer s.hub.unsubscribe(ch)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 2000\n: connected\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: change\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}
