package server

import (
	"context"
	"net/http"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
)

// archiveActor signs the archiving the server does on its own.
var archiveActor = board.Actor{Kind: "person", Name: "quest-map (auto-archive)"}

// archiveEvery is how often done tasks are checked for auto-archiving.
const archiveEvery = time.Hour

// autoArchive archives done tasks of projects that set archive_done_after_days, at start and then every hour,
// until ctx ends; it closes done when it returns (Close waits for that before it releases the board lock).
func (s *Server) autoArchive(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(archiveEvery)
	defer t.Stop()
	for {
		if n, err := s.archiveSweep(""); err != nil {
			s.logf("auto-archive: %v", err)
		} else if n > 0 {
			s.logf("auto-archive: archived %d done task(s)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// archiveSweep archives the done tasks that have not changed for the project's archive_done_after_days ("" =
// every project). It returns how many it archived; one task that fails does not stop the others.
func (s *Server) archiveSweep(slug string) (int, error) {
	s.archiveMu.Lock() // the hourly sweep and a settings change do not sweep at once (counts stay right)
	defer s.archiveMu.Unlock()
	ps, err := s.Board.Projects()
	if err != nil {
		return 0, err
	}
	now := s.Board.Now()
	n := 0
	var firstErr error
	for _, p := range ps {
		days := p.Config().ArchiveDoneAfterDays
		if days <= 0 || (slug != "" && p.Slug() != slug) {
			continue
		}
		age := time.Duration(days) * 24 * time.Hour
		rows, err := s.Index.FindString("status:done", index.FindOptions{Project: p.Slug()})
		if err != nil {
			return n, err
		}
		for _, r := range rows {
			// A cheap pre-check on the index; ArchiveDoneFor decides under the project lock, also counting
			// activity records (hand edits) as changes.
			if !r.Updated.IsZero() && now.Sub(r.Updated) < age {
				continue
			}
			done, err := p.ArchiveDoneFor(r.ID, archiveActor, age)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if done {
				s.refreshTask(p, r.ID)
				n++
			}
		}
	}
	return n, firstErr
}

// apiSetArchive sets a project's archive_done_after_days and archives what is already due.
func (s *Server) apiSetArchive(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Days int `json:"days"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if err := p.SetArchiveDays(in.Days); err != nil {
		return nil, badRequest("%v", err)
	}
	if err := s.Index.RefreshProject(p.Slug()); err != nil {
		return nil, err
	}
	n, err := s.archiveSweep(p.Slug())
	return map[string]int{"archived": n}, err
}
