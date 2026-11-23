package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/boardsync"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/plugins"
	"github.com/RoyalNoob/quest-map/internal/suite"
)

// The REST API the web UI uses. Changes made here are a person's: the actor is the local user.

// personActor names the person using the web UI: the OS user name, without a domain.
func personActor() board.Actor {
	name := "person"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
		if i := strings.LastIndexAny(name, `\/`); i >= 0 {
			name = name[i+1:]
		}
	}
	return board.Actor{Kind: "person", Name: name}
}

type apiError struct {
	status int
	msg    string
}

func (e apiError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return apiError{http.StatusBadRequest, fmt.Sprintf(format, args...)}
}

// apiFunc handlers return a value to send as JSON, or an error.
type apiFunc func(r *http.Request) (any, error)

func (s *Server) api(fn apiFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r)
		if err != nil {
			status := http.StatusInternalServerError
			var ae apiError
			var pe *board.ParseError
			switch {
			case errors.As(err, &ae):
				status = ae.status
			case errors.Is(err, board.ErrNotFound):
				status = http.StatusNotFound
			case errors.Is(err, board.ErrConflict):
				status = http.StatusConflict
			case errors.As(err, &pe):
				status = http.StatusConflict
			case isUserError(err):
				status = http.StatusBadRequest
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		writeJSON(w, v)
	}
}

// isUserError reports board validation errors (bad values, rule violations) as opposed to I/O failures.
func isUserError(err error) bool {
	var pathErr *os.PathError
	return !errors.As(err, &pathErr)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("bad request body: %v", err)
	}
	return nil
}

// projectOfID finds the project whose key prefixes a task id.
func (s *Server) projectOfID(id string) (*board.Project, string, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	i := strings.LastIndex(id, "-")
	if i <= 0 {
		return nil, id, badRequest("%q is not a task id like RPG-12", id)
	}
	ps, err := s.Board.Projects()
	if err != nil {
		return nil, id, err
	}
	for _, p := range ps {
		if p.Key() == id[:i] {
			return p, id, nil
		}
	}
	return nil, id, fmt.Errorf("no project has the key %s: %w", id[:i], board.ErrNotFound)
}

func (s *Server) projectArg(name string) (*board.Project, error) {
	ps, err := s.Board.Projects()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if strings.EqualFold(p.Slug(), name) || strings.EqualFold(p.Key(), name) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("project %q: %w", name, board.ErrNotFound)
}

// refreshTask updates the index right after a write (which also sends the live event).
func (s *Server) refreshTask(p *board.Project, id string) {
	s.Index.RefreshAfterWrite(p.TaskPath(id, false))
	s.Index.RefreshAfterWrite(p.TaskPath(id, true))
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/meta", s.api(s.apiMeta))
	mux.HandleFunc("GET /api/projects", s.api(s.apiProjects))
	mux.HandleFunc("GET /api/warnings", s.api(s.apiWarnings))
	mux.HandleFunc("GET /api/events", s.serveEvents)
	mux.HandleFunc("POST /api/tasks", s.api(s.apiCreate))
	mux.HandleFunc("GET /api/tasks/{id}", s.api(s.apiShow))
	mux.HandleFunc("GET /api/tasks/{id}/locks", s.api(s.apiLocks))
	mux.HandleFunc("PUT /api/projects/{slug}/workdir", s.api(s.apiSetWorkdir))
	mux.HandleFunc("PATCH /api/tasks/{id}", s.api(s.apiEdit))
	mux.HandleFunc("DELETE /api/tasks/{id}", s.api(s.apiDelete))
	mux.HandleFunc("POST /api/tasks/{id}/notes", s.api(s.apiNote))
	mux.HandleFunc("POST /api/tasks/{id}/usage", s.api(s.apiUsage))
	mux.HandleFunc("POST /api/tasks/{id}/release", s.api(s.apiRelease))
	mux.HandleFunc("POST /api/tasks/{id}/archive", s.api(s.apiArchive))
	mux.HandleFunc("POST /api/tasks/{id}/move", s.api(s.apiMove))
	mux.HandleFunc("POST /api/tasks/{id}/attachments", s.api(s.apiAttach))
	mux.HandleFunc("GET /files/{project}/{id}/{name}", s.serveAttachment)
}

// ---- reads ----

func (s *Server) apiMeta(r *http.Request) (any, error) {
	return map[string]any{
		"version":       s.version,
		"user":          s.person.Name,
		"custom_css":    s.css,
		"claim_ttl_sec": int64(s.claimTTL / time.Second),
		"disciplines":   []string{"code", "blueprint", "design", "level", "art", "anim", "vfx", "audio", "ui", "qa"},
		"statuses":      board.Statuses,
		"max_upload":    board.MaxAttachment,
		"timezone":      boardZone(s.Board.Location()),
	}, nil
}

type milestoneJSON struct {
	Name string `json:"name"`
	Date string `json:"date,omitempty"`
}

type projectJSON struct {
	Slug        string          `json:"slug"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Repos       []string        `json:"repos"`
	Milestones  []milestoneJSON `json:"milestones"`
	Suite       *suite.Suite    `json:"suite"`
	SuiteError  string          `json:"suite_error,omitempty"`
	Open        int             `json:"open"`           // tasks not done and not archived, for the sidebar
	ArchiveDays int             `json:"archive_days"`   // archive_done_after_days; 0 = never
	Workdir     string          `json:"workdir"`        // working folder on this machine for files and locks (§5)
	WorkdirSet  bool            `json:"workdir_pinned"` // a person chose it in Settings (else: an agent's last folder)
}

func (s *Server) apiProjects(r *http.Request) (any, error) {
	ps, err := s.Board.Projects()
	if err != nil {
		return nil, err
	}
	open := map[string]int{}
	rows, err := s.Index.FindString("-status:done", index.FindOptions{})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		open[row.Project]++
	}
	out := []projectJSON{}
	for _, p := range ps {
		cfg := p.Config()
		pj := projectJSON{Slug: p.Slug(), Key: p.Key(), Name: cfg.Name, Repos: cfg.Repos, Milestones: []milestoneJSON{}, Open: open[p.Slug()], ArchiveDays: cfg.ArchiveDoneAfterDays}
		pj.Workdir, pj.WorkdirSet = s.MCP.Workdir(p.Slug())
		if pj.Repos == nil {
			pj.Repos = []string{}
		}
		for _, m := range cfg.Milestones {
			mj := milestoneJSON{Name: m.Name}
			if m.Date != nil {
				mj.Date = m.Date.String()
			}
			pj.Milestones = append(pj.Milestones, mj)
		}
		pj.Suite, pj.SuiteError = s.suiteOf(p)
		out = append(out, pj)
	}
	return out, nil
}

func (s *Server) apiWarnings(r *http.Request) (any, error) {
	probs, err := s.Index.ParseProblems()
	if err != nil {
		return nil, err
	}
	type parseJSON struct {
		Path string `json:"path"`
		ID   string `json:"id,omitempty"`
		Err  string `json:"error"`
	}
	out := struct {
		Parse      []parseJSON        `json:"parse"`
		Stale      []string           `json:"stale"`
		Renumbered []index.Renumbered `json:"renumbered"`
		Suites     string             `json:"suites,omitempty"` // a suites/ file that does not load (§4.1)
		I18n       []i18nProblem      `json:"i18n"`             // board language files with problems (§4.4)
		Plugins    []plugins.Problem  `json:"plugins"`          // plugins that did not load (§8.5)
		Sync       string             `json:"sync,omitempty"`   // the last board sync ended in conflict or error (§3.5)
	}{Parse: []parseJSON{}, Stale: []string{}, Renumbered: s.Index.Renumbered(), I18n: s.i18nProblems()}
	if _, out.Plugins = s.loadPlugins(); out.Plugins == nil {
		out.Plugins = []plugins.Problem{}
	}
	if _, err := s.MCP.Suites().Registry(); err != nil {
		out.Suites = err.Error()
	}
	if st := s.syncer.Status(); st.State == boardsync.StateConflict || st.State == boardsync.StateError {
		out.Sync = st.Message
	}
	for _, p := range probs {
		out.Parse = append(out.Parse, parseJSON{Path: p.Path, ID: p.ID, Err: p.Err})
	}
	rows, err := s.Index.FindString("stale:yes", index.FindOptions{})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Stale = append(out.Stale, r.ID)
	}
	if out.Renumbered == nil {
		out.Renumbered = []index.Renumbered{}
	}
	return out, nil
}

type logJSON struct {
	Time string `json:"time"`
	Who  string `json:"who"`
	Text string `json:"text"`
}

type spentJSON struct {
	Tokens  board.Tokens            `json:"tokens"`
	Total   int64                   `json:"total"`
	Seconds int64                   `json:"seconds"`
	Records int                     `json:"records"`
	Self    int                     `json:"self"`
	CostUSD float64                 `json:"cost_usd,omitempty"`
	Models  map[string]board.Tokens `json:"models"`
}

type historyJSON struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Actor   string    `json:"actor,omitempty"`
	Session string    `json:"session,omitempty"`
	Model   string    `json:"model,omitempty"`
	Source  string    `json:"source,omitempty"`
	Tokens  int64     `json:"tokens,omitempty"`
	Seconds int64     `json:"seconds,omitempty"`
	Fields  []string  `json:"fields,omitempty"`
}

type taskJSON struct {
	ID         string            `json:"id"`
	Project    string            `json:"project"`
	Title      string            `json:"title"`
	Status     board.Status      `json:"status"`
	Doer       board.Doer        `json:"doer"`
	Assignees  []string          `json:"assignees"`
	Fields     map[string]string `json:"fields"` // custom fields (suite)
	Priority   board.Priority    `json:"priority"`
	Discipline string            `json:"discipline"`
	Milestone  string            `json:"milestone"`
	Labels     []string          `json:"labels"`
	Asset      string            `json:"asset"`
	Pipeline   *board.Pipeline   `json:"pipeline"`
	FoundIn    string            `json:"found_in"`
	FixedIn    string            `json:"fixed_in"`
	BlockedBy  []string          `json:"blocked_by"`
	Blockers   []index.Link      `json:"blockers"` // blocked_by with each task's title and status
	Blocks     []index.Link      `json:"blocks"`   // open tasks waiting on this one
	MovedFrom  string            `json:"moved_from"`
	URL        string            `json:"url"`
	Claim      *board.Claim      `json:"claim"`
	Created    time.Time         `json:"created"`
	Archived   bool              `json:"archived"`
	Body       string            `json:"body"`
	Log        []logJSON         `json:"log"`
	Spent      spentJSON         `json:"spent"`
	History    []historyJSON     `json:"history"`
	Warning    string            `json:"warning,omitempty"`
	Version    string            `json:"version"`
}

// apiSetWorkdir pins the working folder this machine's web UI reads a project's files and locks in ("" unpins).
func (s *Server) apiSetWorkdir(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Dir string `json:"dir"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if err := s.MCP.SetWorkdir(p.Slug(), in.Dir); err != nil {
		return nil, badRequest("%v", err)
	}
	dir, pinned := s.MCP.Workdir(p.Slug())
	return map[string]any{"workdir": dir, "workdir_pinned": pinned}, nil
}

// apiLocks returns a task's files and their lock state (spec §5), read in the working folder an agent last
// used the project from. It is asked separately from apiShow so a slow lock server never holds up the drawer.
func (s *Server) apiLocks(r *http.Request) (any, error) {
	dir, files, ls, err := s.MCP.TaskLocks(r.Context(), r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []string{}
	}
	return map[string]any{"workdir": dir, "files": files, "locks": ls}, nil
}

func (s *Server) apiShow(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	t, err := p.Get(id)
	if err != nil {
		var pe *board.ParseError
		if errors.As(err, &pe) {
			return nil, apiError{http.StatusConflict, "the task file cannot be read: " + pe.Err.Error() + " – fix it in an editor; quest-map will not overwrite it"}
		}
		return nil, err
	}
	tj := taskJSON{ID: t.ID, Project: p.Slug(), Title: t.Title, Status: t.Status, Doer: t.Doer, Assignees: nonNil(t.Assignees), Fields: nonNilMap(t.Custom), Priority: t.Priority,
		Discipline: t.Discipline, Milestone: t.Milestone, Labels: nonNil(t.Labels), Asset: t.Asset, Pipeline: t.Pipeline,
		FoundIn: t.FoundIn, FixedIn: t.FixedIn, BlockedBy: nonNil(t.BlockedBy), MovedFrom: t.MovedFrom, URL: t.URL,
		Claim: t.Claim, Created: t.Created, Archived: t.Archived, Body: t.Body, Log: []logJSON{}, History: []historyJSON{},
		Version: t.Version()}
	loc := s.Board.Location()
	for _, e := range t.Log {
		tj.Log = append(tj.Log, logJSON{Time: e.Time.In(loc).Format("2006-01-02 15:04"), Who: e.Who, Text: e.Text})
	}
	if sp, err := p.Spent(id); err == nil {
		tj.Spent = spentJSON{Tokens: sp.Tokens, Total: sp.Tokens.Sum(), Seconds: int64(sp.Time / time.Second), Records: sp.Records,
			Self: sp.Self, CostUSD: sp.CostUSD, Models: sp.Models}
	}
	if tj.Spent.Models == nil {
		tj.Spent.Models = map[string]board.Tokens{}
	}
	if recs, _, err := p.Activity(); err == nil {
		for _, rec := range recs {
			if rec.TaskID() != id || (rec.Kind != board.KindStatus && rec.Kind != board.KindUsage) {
				continue
			}
			h := historyJSON{At: rec.At, Kind: rec.Kind, From: rec.From, To: rec.To, Actor: rec.Actor, Session: rec.Session,
				Model: rec.Model, Source: rec.Source, Seconds: rec.TimeS}
			if rec.Tokens != nil {
				h.Tokens = rec.Tokens.Sum()
			}
			tj.History = append(tj.History, h)
		}
	}
	if tj.Blockers, tj.Blocks, err = s.Index.Links(id); err != nil {
		return nil, err
	}
	if probs, err := s.Index.ParseProblems(); err == nil {
		for _, pr := range probs {
			if pr.ID == id {
				tj.Warning = pr.Err
			}
		}
	}
	return tj, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- writes ----

type createJSON struct {
	Project    string            `json:"project"`
	Title      string            `json:"title"`
	Body       string            `json:"body"`
	Status     string            `json:"status"`
	Doer       string            `json:"doer"`
	Assignees  []string          `json:"assignees"`
	Priority   string            `json:"priority"`
	Discipline string            `json:"discipline"`
	Milestone  string            `json:"milestone"`
	Labels     []string          `json:"labels"`
	Asset      string            `json:"asset"`
	BlockedBy  []string          `json:"blocked_by"`
	Pipeline   *board.Pipeline   `json:"pipeline"` // as in edits: {name, at}
	Fields     map[string]string `json:"fields"`
}

func (s *Server) apiCreate(r *http.Request) (any, error) {
	var in createJSON
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	p, err := s.projectArg(in.Project)
	if err != nil {
		return nil, err
	}
	if err := s.MCP.CheckFields(p, in.Fields); err != nil {
		return nil, badRequest("%v", err)
	}
	t, err := p.Create(&board.Task{Title: in.Title, Body: in.Body, Status: board.Status(in.Status), Doer: board.Doer(in.Doer), Assignees: board.CleanNames(in.Assignees), Custom: board.CleanCustom(in.Fields),
		Priority: board.Priority(in.Priority), Discipline: in.Discipline, Milestone: in.Milestone, Labels: cleanList(in.Labels, false),
		Asset: in.Asset, BlockedBy: cleanList(in.BlockedBy, true), Pipeline: in.Pipeline}, s.person, nil)
	if err != nil {
		return nil, badRequest("%v", err)
	}
	s.refreshTask(p, t.ID)
	return map[string]string{"id": t.ID}, nil
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func cleanList(in []string, upper bool) []string {
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if upper {
			v = strings.ToUpper(v)
		}
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

type editJSON struct {
	Title      *string           `json:"title"`
	Body       *string           `json:"body"`
	Status     *string           `json:"status"`
	Doer       *string           `json:"doer"`
	Assignees  *[]string         `json:"assignees"`
	Fields     map[string]string `json:"fields"` // custom fields; "" clears one
	Priority   *string           `json:"priority"`
	Discipline *string           `json:"discipline"`
	Milestone  *string           `json:"milestone"`
	Labels     *[]string         `json:"labels"`
	Asset      *string           `json:"asset"`
	Pipeline   *board.Pipeline   `json:"pipeline"`
	FoundIn    *string           `json:"found_in"`
	FixedIn    *string           `json:"fixed_in"`
	BlockedBy  *[]string         `json:"blocked_by"`
	URL        *string           `json:"url"`
	Note       string            `json:"note"`
	// Single-entry list changes that never drop what others added meanwhile (table, quick pickers, bulk edits).
	AddLabels       []string `json:"add_labels"`
	RemoveLabels    []string `json:"remove_labels"`
	AddAssignees    []string `json:"add_assignees"`
	RemoveAssignees []string `json:"remove_assignees"`
	// Version is the task version the edit was made on (from GET or a list row); the edit is refused with
	// 409 if the file changed since. Empty skips the check.
	Version string `json:"version"`
}

func (s *Server) apiEdit(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var in editJSON
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if err := s.MCP.CheckFields(p, in.Fields); err != nil {
		return nil, badRequest("%v", err)
	}
	e := board.Edit{AddLabels: in.AddLabels, RemoveLabels: in.RemoveLabels, AddAssignees: in.AddAssignees, RemoveAssignees: in.RemoveAssignees,
		Title: in.Title, Body: in.Body, Assignees: in.Assignees, Custom: in.Fields, Discipline: in.Discipline, Milestone: in.Milestone, Asset: in.Asset,
		Pipeline: in.Pipeline, FoundIn: in.FoundIn, FixedIn: in.FixedIn, URL: in.URL}
	if in.Status != nil {
		st := board.Status(*in.Status)
		e.Status = &st
	}
	if in.Doer != nil {
		d := board.Doer(*in.Doer)
		e.Doer = &d
	}
	if in.Priority != nil {
		pr := board.Priority(*in.Priority)
		e.Priority = &pr
	}
	if in.Labels != nil {
		l := cleanList(*in.Labels, false)
		e.Labels = &l
	}
	if in.BlockedBy != nil {
		b := cleanList(*in.BlockedBy, true)
		e.BlockedBy = &b
	}
	if _, err := p.UpdateIf(id, s.person, e, in.Note, nil, board.CheckVersion(in.Version)); err != nil {
		return nil, err
	}
	s.refreshTask(p, id)
	return nil, nil
}

func (s *Server) apiNote(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if _, err := p.Note(id, s.person, in.Text, nil); err != nil {
		return nil, err
	}
	s.refreshTask(p, id)
	return nil, nil
}

// apiUsage adds a person's usage correction (positive or negative) to a task.
func (s *Server) apiUsage(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Tokens int64  `json:"tokens"`
		Time   string `json:"time"`
		Note   string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	u := &board.Usage{Tokens: board.Tokens{Total: in.Tokens}, Source: board.SourceHuman}
	if strings.TrimSpace(in.Time) != "" {
		if u.Time, err = board.ParseDuration(in.Time); err != nil {
			return nil, badRequest("time: %v", err)
		}
	}
	if u.IsZero() {
		return nil, badRequest("give tokens or time")
	}
	note := strings.TrimSpace(in.Note)
	if note == "" {
		note = "usage correction"
	}
	if _, err := p.Note(id, s.person, note, u); err != nil {
		return nil, err
	}
	s.refreshTask(p, id)
	return nil, nil
}

func (s *Server) apiRelease(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			return nil, err
		}
	}
	if _, err := p.Release(id, s.person, in.Note, nil); err != nil {
		return nil, err
	}
	s.refreshTask(p, id)
	return nil, nil
}

func (s *Server) apiArchive(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	if err := p.Archive(id, s.person); err != nil {
		return nil, err
	}
	s.refreshTask(p, id)
	return nil, nil
}

func (s *Server) apiMove(r *http.Request) (any, error) {
	src, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var in struct {
		To string `json:"to"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	dst, err := s.projectArg(in.To)
	if err != nil {
		return nil, err
	}
	t, err := s.Board.Move(src, id, dst, s.person)
	if t == nil {
		return nil, err
	}
	s.refreshTask(src, id)
	s.refreshTask(dst, t.ID)
	out := map[string]string{"id": t.ID}
	if err != nil {
		// The task moved; only a follow-up step (attachments, blocked_by in other tasks) failed.
		out["warning"] = err.Error()
	}
	return out, nil
}

func (s *Server) apiDelete(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	if err := p.Delete(id, s.person); err != nil {
		return nil, err
	}
	s.refreshTask(p, id)
	return nil, nil
}

// apiAttach stores the request body as an attachment: POST /api/tasks/RPG-2/attachments?name=shot.png.
// It returns the saved name and a Markdown link (relative to the task file) to put in the body.
func (s *Server) apiAttach(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, board.MaxAttachment+1))
	if err != nil {
		return nil, err
	}
	if len(data) > board.MaxAttachment {
		return nil, apiError{http.StatusRequestEntityTooLarge, fmt.Sprintf("attachments can be at most %d MB", board.MaxAttachment>>20)}
	}
	if len(data) == 0 {
		return nil, badRequest("empty attachment")
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "pasted"
		if exts, _ := mime.ExtensionsByType(r.Header.Get("Content-Type")); len(exts) > 0 {
			name += exts[len(exts)-1]
		}
	}
	saved, err := p.SaveAttachment(id, name, data)
	if err != nil {
		return nil, badRequest("%v", err)
	}
	link := "../attachments/" + id + "/" + saved
	md := "[" + saved + "](" + link + ")"
	if ct := http.DetectContentType(data); strings.HasPrefix(ct, "image/") {
		md = "!" + md
	}
	return map[string]string{"name": saved, "markdown": md, "url": "/files/" + p.Slug() + "/" + id + "/" + saved}, nil
}

// serveAttachment serves a task attachment to the UI.
func (s *Server) serveAttachment(w http.ResponseWriter, r *http.Request) {
	p, err := s.projectArg(r.PathValue("project"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, err := p.AttachmentPath(strings.ToUpper(r.PathValue("id")), r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	if !strings.HasPrefix(mime.TypeByExtension(filepath.Ext(path)), "image/") {
		w.Header().Set("Content-Disposition", "attachment")
	}
	http.ServeFile(w, r, path)
}

// boardZone is the board's timezone as an IANA name for the browser (plugins count days in it, §8.5); "" for
// Local, which the browser on this machine already uses.
func boardZone(loc *time.Location) string {
	if loc == nil || loc == time.Local {
		return ""
	}
	return loc.String()
}
