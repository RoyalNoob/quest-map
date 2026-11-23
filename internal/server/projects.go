package server

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/mcpserver"
)

// REST for managing projects from the web UI: create, rename and delete. Create follows project_create;
// rename and delete have no MCP tools on purpose – agents never delete projects.

func (s *Server) projectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects", s.api(s.apiCreateProject))
	mux.HandleFunc("PUT /api/projects/{slug}/name", s.api(s.apiSetProjectName))
	mux.HandleFunc("DELETE /api/projects/{slug}", s.api(s.apiDeleteProject))
}

// apiCreateProject adds a project like the project_create tool, and can also pick its suite.
func (s *Server) apiCreateProject(r *http.Request) (any, error) {
	var in struct {
		mcpserver.ProjectCreateIn
		Suite string `json:"suite"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Key) == "" {
		return nil, badRequest("give the project a key, e.g. RPG")
	}
	if in.Suite = strings.TrimSpace(in.Suite); in.Suite != "" {
		reg, _ := s.MCP.Suites().Registry()
		if reg == nil {
			return nil, errors.New("suites could not be loaded")
		}
		if _, err := reg.Get(in.Suite); err != nil {
			return nil, badRequest("%v", err)
		}
	}
	_, out, err := s.MCP.ProjectCreate(mcpserver.Caller{}, in.ProjectCreateIn)
	if err != nil {
		// Also "project X created, but linking the repo failed": the UI reloads the projects either way.
		return nil, badRequest("%v", err)
	}
	p, err := s.Board.Project(out.Slug)
	if err != nil {
		return nil, err
	}
	if in.Suite != "" {
		if err := p.SetSuite(in.Suite); err != nil {
			return nil, err
		}
	}
	if err := s.Index.RefreshProject(p.Slug()); err != nil {
		return nil, err
	}
	return map[string]string{"slug": p.Slug(), "key": p.Key(), "name": p.Config().Name}, nil
}

func (s *Server) apiSetProjectName(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if err := p.SetName(in.Name); err != nil {
		return nil, badRequest("%v", err)
	}
	s.Index.Notify(index.Change{Kind: "project", Project: p.Slug()})
	return map[string]string{"name": p.Config().Name}, nil
}

// apiDeleteProject moves a project's folder to the board's trash (board.TrashProject). The request names
// the project's key in confirm, so a stray request cannot delete one, and it is refused while an agent
// holds a live claim on one of its tasks.
func (s *Server) apiDeleteProject(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(in.Confirm), p.Key()) {
		return nil, badRequest("to delete project %s, send its key %s in confirm", p.Slug(), p.Key())
	}
	rows, err := s.Index.FindString("status:doing -stale:yes", index.FindOptions{Project: p.Slug()})
	if err != nil {
		return nil, err
	}
	var claimed []string
	for _, row := range rows {
		if row.ClaimBy != "" {
			claimed = append(claimed, row.ID)
		}
	}
	if len(claimed) > 0 {
		return nil, apiError{http.StatusConflict, "agents are working on " + strings.Join(claimed, ", ") +
			" – release those tasks (or wait until the claims go stale) before deleting the project"}
	}
	dir := filepath.Join(s.Board.Root(), "projects", p.Slug())
	if s.watcher != nil {
		s.watcher.Unwatch(dir) // Windows will not move a folder whose subfolders are watched
	}
	trashed, err := s.Board.TrashProject(p.Slug())
	if err != nil {
		if s.watcher != nil {
			s.watcher.Rewatch(dir)
		}
		return nil, err
	}
	if err := s.Index.RefreshGone(dir); err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(s.Board.Root(), trashed)
	return map[string]string{"trashed": filepath.ToSlash(rel)}, nil
}
