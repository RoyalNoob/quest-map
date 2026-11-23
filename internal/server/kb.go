package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/kb"
)

// REST for the knowledge base (§4.5), mirroring kb_find / kb_show / kb_write for the web UI. Freshness is
// checked against the project's working folder (Settings, or where an agent last worked); without one it is
// unknown.

func (s *Server) kbRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/kb", s.api(s.apiKBFind))
	mux.HandleFunc("POST /api/kb/{project}", s.api(s.apiKBCreate))
	mux.HandleFunc("GET /api/kb/{project}/{slug}", s.api(s.apiKBShow))
	mux.HandleFunc("PUT /api/kb/{project}/{slug}", s.api(s.apiKBEdit))
	mux.HandleFunc("GET /api/tasks/{id}/kb", s.api(s.apiTaskKB))
}

type pageJSON struct {
	kb.Result
	Body    string `json:"body,omitempty"`
	Version string `json:"version,omitempty"`
}

func (s *Server) workdirOf(slug string) string {
	dir, _ := s.MCP.Workdir(slug)
	return dir
}

// apiKBFind: ?project= (default all), ?q= (free words, tag:, source:, stale:, retired:).
func (s *Server) apiKBFind(r *http.Request) (any, error) {
	q, err := kb.ParseQuery(r.URL.Query().Get("q"))
	if err != nil {
		return nil, badRequest("%s", err)
	}
	if name := r.URL.Query().Get("project"); name != "" {
		p, err := s.projectArg(name)
		if err != nil {
			return nil, err
		}
		q.Project = p.Slug()
	}
	q.Limit = 200
	res, err := kb.Find(s.Index, q, s.workdirOf)
	if err != nil {
		return nil, err
	}
	if res == nil {
		res = []kb.Result{}
	}
	return res, nil
}

func (s *Server) pageOf(r *http.Request) (*board.Project, string, error) {
	p, err := s.projectArg(r.PathValue("project"))
	return p, r.PathValue("slug"), err
}

// rowOf returns the index row of a page (with its version) for a freshness check.
func (s *Server) rowOf(p *board.Project, pg *board.Page) index.PageRow {
	if rows, _ := s.Index.FindPages(index.PageQuery{Project: p.Slug(), Slug: pg.Slug, Retired: true, Limit: 1}); len(rows) > 0 {
		return rows[0]
	}
	return index.PageRow{Project: p.Slug(), Slug: pg.Slug, Path: p.PagePath(pg.Slug), Title: pg.Title, Tags: pg.Tags,
		Question: pg.Question, Sources: pg.Sources, Checked: pg.Checked, Retired: pg.Retired, Hash: pg.Version()}
}

func (s *Server) showPage(p *board.Project, slug string) (any, error) {
	pg, err := p.Page(slug)
	var pe *board.ParseError
	if errors.As(err, &pe) {
		return nil, apiError{http.StatusConflict, "the page file cannot be read: " + pe.Err.Error() + " – fix it in an editor; quest-map will not overwrite it"}
	}
	if err != nil {
		return nil, err
	}
	row := s.rowOf(p, pg)
	return pageJSON{Result: kb.Result{PageRow: row, Freshness: kb.Check(s.Index, row, s.workdirOf(p.Slug()))}, Body: pg.Body, Version: pg.Version()}, nil
}

func (s *Server) apiKBShow(r *http.Request) (any, error) {
	p, slug, err := s.pageOf(r)
	if err != nil {
		return nil, err
	}
	return s.showPage(p, slug)
}

// pageBody is a page edit from the UI. Sources are "task:RPG-12" or repo paths.
type pageBody struct {
	Title    *string   `json:"title"`
	Question *string   `json:"question"`
	Body     *string   `json:"body"`
	Tags     *[]string `json:"tags"`
	Sources  *[]string `json:"sources"`
	Retired  *bool     `json:"retired"`
	Version  string    `json:"version"`
}

func (b pageBody) edit() board.PageEdit {
	e := board.PageEdit{Title: b.Title, Question: b.Question, Body: b.Body, Tags: b.Tags, Retired: b.Retired, Version: b.Version}
	if b.Sources != nil {
		var src []board.Source
		for _, v := range *b.Sources {
			if id, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(v)), "task:"); ok {
				src = append(src, board.Source{Task: id})
			} else {
				src = append(src, board.Source{Path: v})
			}
		}
		e.Sources = &src
	}
	return e
}

func decodePage(r *http.Request) (pageBody, error) {
	var b pageBody
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, board.MaxPageBody+64<<10)).Decode(&b); err != nil {
		return b, badRequest("page: %s", err)
	}
	return b, nil
}

func (s *Server) writePage(p *board.Project, slug string, create bool, b pageBody) (any, error) {
	_, _, err := p.WritePage(slug, create, b.edit(), s.person)
	switch {
	case errors.Is(err, board.ErrPageExists):
		return nil, apiError{http.StatusConflict, "a page named " + slug + " exists; open it instead"}
	case errors.Is(err, board.ErrConflict):
		return nil, apiError{http.StatusConflict, err.Error()}
	case err != nil:
		return nil, badRequest("%s", err)
	}
	s.Index.RefreshPage(p.PagePath(slug))
	return s.showPage(p, slug)
}

// apiKBCreate makes a page named after its title.
func (s *Server) apiKBCreate(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("project"))
	if err != nil {
		return nil, err
	}
	b, err := decodePage(r)
	if err != nil {
		return nil, err
	}
	if b.Title == nil || board.Slugify(*b.Title) == "" {
		return nil, badRequest("a new page needs a title with letters or digits")
	}
	return s.writePage(p, board.Slugify(*b.Title), true, b)
}

// apiKBEdit changes a page. A person's edit does not move its check: only an agent looking at a working copy
// can say the answer still holds.
func (s *Server) apiKBEdit(r *http.Request) (any, error) {
	p, slug, err := s.pageOf(r)
	if err != nil {
		return nil, err
	}
	b, err := decodePage(r)
	if err != nil {
		return nil, err
	}
	return s.writePage(p, slug, false, b)
}

// apiTaskKB lists the pages that name a task as a source (the task drawer's Knowledge section).
func (s *Server) apiTaskKB(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	rows, err := s.Index.PagesForTask(id)
	if err != nil {
		return nil, err
	}
	out := []kb.Result{}
	for _, row := range rows {
		out = append(out, kb.Result{PageRow: row, Freshness: kb.Check(s.Index, row, s.workdirOf(p.Slug()))})
	}
	return out, nil
}
