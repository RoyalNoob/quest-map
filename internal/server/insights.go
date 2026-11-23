package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/insights"
)

// Insights (§8.4): the metrics, and the raw activity records for Grafana or a spreadsheet.

func (s *Server) insightsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/insights", s.api(s.apiInsights))
	mux.HandleFunc("GET /api/activity", s.api(s.apiActivity))
}

// apiInsights: ?project=&since=&by=&task=
func (s *Server) apiInsights(r *http.Request) (any, error) {
	q := r.URL.Query()
	var p *board.Project
	if name := q.Get("project"); name != "" {
		var err error
		if p, err = s.projectArg(name); err != nil {
			return nil, err
		}
	} else if ps, err := s.Board.Projects(); err != nil {
		return nil, err
	} else if len(ps) != 1 {
		return nil, badRequest("pass project: the board has %d projects", len(ps))
	} else {
		p = ps[0]
	}
	rep, err := insights.Build(s.Board, p, insights.Query{Since: q.Get("since"), By: q.Get("by"), Task: q.Get("task")}, s.claimTTL)
	if err != nil {
		return nil, err // bad since/by: 400; a file that cannot be read: 500
	}
	return rep, nil
}

// activityJSON is a record with its project, so records of several projects can be told apart.
type activityJSON struct {
	Project string `json:"project"`
	board.Record
}

// apiActivity returns raw activity records in time order: ?project= (default all), ?since=, ?until= (a date: up
// to the end of that day), ?kinds=status,edit. With ?follow=yes a record's task is the task that owns it now,
// through moves and renumbers, and ?project= keeps the records whose task is in that project now (the plugin
// API, §8.5).
func (s *Server) apiActivity(r *http.Request) (any, error) {
	q := r.URL.Query()
	since, err := insights.ParseSince(q.Get("since"), s.Board.Now(), s.Board.Location())
	if err != nil {
		return nil, badRequest("%s", err)
	}
	var until time.Time
	if u := q.Get("until"); u != "" {
		d, err := time.ParseInLocation("2006-01-02", u, s.Board.Location())
		if err != nil {
			return nil, badRequest("until %q: use a date like 2026-11-30", u)
		}
		until = d.AddDate(0, 0, 1)
	}
	kinds := map[string]bool{}
	for _, k := range strings.Split(q.Get("kinds"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds[k] = true
		}
	}
	follow := q.Get("follow") == "yes"
	ps, err := s.Board.Projects()
	if err != nil {
		return nil, err
	}
	var only *board.Project
	if name := q.Get("project"); name != "" {
		if only, err = s.projectArg(name); err != nil {
			return nil, err
		}
		if !follow {
			ps = []*board.Project{only}
		}
	}
	var ledger *board.Ledger
	if follow {
		if ledger, err = s.Board.Ledger(); err != nil {
			return nil, err
		}
	}
	out := []activityJSON{}
	for _, p := range ps {
		recs, _, err := p.Activity()
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			if rec.At.Before(since) || (!until.IsZero() && !rec.At.Before(until)) || (len(kinds) > 0 && !kinds[rec.Kind]) {
				continue
			}
			slug := p.Slug()
			if follow && rec.Task != nil {
				owner := ledger.Owner(rec)
				rec.Task = &owner
				if op, _, err := s.projectOfID(owner); err == nil {
					slug = op.Slug()
				}
			}
			if only != nil && slug != only.Slug() {
				continue
			}
			out = append(out, activityJSON{Project: slug, Record: rec})
		}
	}
	if follow { // records of several files: keep time order
		sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	}
	return out, nil
}
