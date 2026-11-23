package server

import (
	"context"
	"net/http"

	"github.com/RoyalNoob/quest-map/internal/boardsync"
)

// Board sync (spec §3.5): the web UI's Sync button and this machine's auto-sync setting.
func (s *Server) syncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sync", s.api(func(r *http.Request) (any, error) {
		return s.syncer.Refresh(r.Context()), nil
	}))
	mux.HandleFunc("POST /api/sync", s.api(func(r *http.Request) (any, error) {
		// The sync outlives a closed tab: a merge cut short would leave the board half merged.
		ctx, cancel := context.WithTimeout(s.syncCtx, 5*boardsync.Timeout)
		defer cancel()
		return s.syncer.Sync(ctx), nil
	}))
	mux.HandleFunc("PUT /api/sync", s.api(func(r *http.Request) (any, error) {
		var in boardsync.Settings
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if err := s.syncer.SetSettings(in); err != nil {
			return nil, badRequest("%v", err)
		}
		return s.syncer.Status(), nil
	}))
}
