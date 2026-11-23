package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/filter"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/mcpserver"
	"github.com/RoyalNoob/quest-map/internal/suite"
)

// REST for the M5 concepts: suites, milestones, releases, item pipelines and feedback.

func (s *Server) m5Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/suites", s.api(s.apiSuites))
	mux.HandleFunc("GET /api/suites/{name}/file", s.api(s.apiSuiteFile))
	mux.HandleFunc("PUT /api/suites/{name}/file", s.api(s.apiPutSuiteFile))
	mux.HandleFunc("DELETE /api/suites/{name}/file", s.api(s.apiDeleteSuiteFile))
	mux.HandleFunc("POST /api/suites/{name}/views", s.api(s.apiAddSuiteEntry))
	mux.HandleFunc("POST /api/suites/{name}/templates", s.api(s.apiAddSuiteEntry))
	mux.HandleFunc("PUT /api/projects/{slug}/suite", s.api(s.apiSetSuite))
	mux.HandleFunc("PUT /api/projects/{slug}/archive", s.api(s.apiSetArchive))
	mux.HandleFunc("PUT /api/projects/{slug}/milestones", s.api(s.apiSetMilestones))
	mux.HandleFunc("GET /api/projects/{slug}/milestones", s.api(s.apiMilestoneSummary))
	mux.HandleFunc("GET /api/projects/{slug}/releases", s.api(s.apiReleases))
	mux.HandleFunc("POST /api/projects/{slug}/releases", s.api(s.apiRecordRelease))
	mux.HandleFunc("POST /api/tasks/{id}/advance", s.api(s.apiAdvance))
	mux.HandleFunc("POST /api/feedback", s.api(s.apiFeedback))
}

// suiteOf returns a project's suite and a problem description ("" if none).
func (s *Server) suiteOf(p *board.Project) (*suite.Suite, string) {
	st, err := s.MCP.Suites().For(s.Board, p)
	if err != nil {
		return st, err.Error()
	}
	return st, ""
}

// fieldKeys returns the custom field keys a filter may use: the project's suite, or every suite's for "".
func (s *Server) fieldKeys(slug string) []string {
	if slug != "" {
		if p, err := s.Board.Project(slug); err == nil {
			if st, _ := s.suiteOf(p); st != nil {
				return st.FieldKeys()
			}
		}
		return nil
	}
	reg, _ := s.MCP.Suites().Registry()
	if reg == nil {
		return nil
	}
	var out []string
	for _, n := range reg.Names() {
		if st, err := reg.Get(n); err == nil {
			out = append(out, st.FieldKeys()...)
		}
	}
	return out
}

func (s *Server) apiSuites(r *http.Request) (any, error) {
	reg, err := s.MCP.Suites().Registry()
	if reg == nil {
		return nil, err
	}
	out := map[string]any{"suites": []*suite.Suite{}, "default": s.Board.Config().DefaultSuite}
	list := []*suite.Suite{}
	for _, n := range reg.Names() {
		st, _ := reg.Get(n)
		list = append(list, st)
	}
	out["suites"] = list
	if err != nil {
		out["error"] = err.Error()
	}
	return out, nil
}

// suiteFilePath returns suites/<name>.toml in the board folder, refusing names that cannot name a suite.
func (s *Server) suiteFilePath(r *http.Request) (string, string, error) {
	name := r.PathValue("name")
	if !suite.ValidName(name) {
		return "", "", badRequest("suite names use lowercase letters, digits and dashes, like %s", suite.Default)
	}
	return name, filepath.Join(s.Board.Root(), "suites", name+".toml"), nil
}

// suiteFileMu serializes the editor's writes to suite files, so two adds never lose one.
var suiteFileMu sync.Mutex

// apiSuiteFile returns the board's file for a suite as typed (empty if there is none), for the suite editor.
func (s *Server) apiSuiteFile(r *http.Request) (any, error) {
	name, path, err := s.suiteFilePath(r)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return map[string]any{"name": name, "builtin": suite.IsBuiltin(name), "exists": err == nil, "text": string(data),
		"base": suite.BuiltinText(name), "version": suiteFileVersion(data, err == nil)}, nil
}

// suiteFileVersion identifies a suite file's content, so a save can tell the file changed since it was opened
// ("" when there is no file).
func suiteFileVersion(data []byte, exists bool) string {
	if !exists {
		return ""
	}
	return board.ContentHash(data)
}

// apiPutSuiteFile checks a suite file and writes it only if it loads, so the editor never leaves a broken
// file behind. Suites reload live, so agents get the change on their next call.
func (s *Server) apiPutSuiteFile(r *http.Request) (any, error) {
	name, path, err := s.suiteFilePath(r)
	if err != nil {
		return nil, err
	}
	var in struct {
		Text    string `json:"text"`
		Version string `json:"version"` // from the GET; "" when the file did not exist
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if err := suite.Check(name, []byte(in.Text)); err != nil {
		return nil, badRequest("%v", err)
	}
	suiteFileMu.Lock()
	defer suiteFileMu.Unlock()
	// Someone else may have saved meanwhile (another tab, "Save to suite", a git pull, a hand edit): refuse
	// rather than overwrite their change.
	cur, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if v := suiteFileVersion(cur, err == nil); v != in.Version {
		return nil, apiError{http.StatusConflict, "suites/" + name + ".toml changed since you opened it; reload it and make your change again"}
	}
	if err := writeSuiteFile(path, []byte(in.Text)); err != nil {
		return nil, err
	}
	return map[string]string{"version": board.ContentHash([]byte(in.Text))}, nil
}

func writeSuiteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return board.WriteFileAtomic(path, data)
}

// apiAddSuiteEntry adds a view (POST …/views) or a template (POST …/templates) to a suite's board file as
// text, keeping the rest of the file as it is ("Save view to suite", "Save as template").
func (s *Server) apiAddSuiteEntry(r *http.Request) (any, error) {
	name, path, err := s.suiteFilePath(r)
	if err != nil {
		return nil, err
	}
	var entry any
	if strings.HasSuffix(r.URL.Path, "/views") {
		var v suite.View
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		entry = v
	} else {
		var t suite.Template
		if err := decode(r, &t); err != nil {
			return nil, err
		}
		entry = t
	}
	suiteFileMu.Lock()
	defer suiteFileMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	out, err := suite.Add(name, data, entry)
	if err != nil {
		return nil, badRequest("%v", err)
	}
	return nil, writeSuiteFile(path, out)
}

// apiDeleteSuiteFile removes the board's file: a built-in suite goes back to as shipped, any other suite is
// gone (projects naming it get the default suite, with a warning).
func (s *Server) apiDeleteSuiteFile(r *http.Request) (any, error) {
	_, path, err := s.suiteFilePath(r)
	if err != nil {
		return nil, err
	}
	suiteFileMu.Lock()
	defer suiteFileMu.Unlock()
	return nil, board.RemoveFile(path)
}

func (s *Server) apiSetSuite(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Suite string `json:"suite"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if in.Suite != "" {
		reg, _ := s.MCP.Suites().Registry()
		if reg == nil {
			return nil, errors.New("suites could not be loaded")
		}
		if _, err := reg.Get(in.Suite); err != nil {
			return nil, badRequest("%v", err)
		}
	}
	if err := p.SetSuite(in.Suite); err != nil {
		return nil, err
	}
	return nil, s.Index.RefreshProject(p.Slug())
}

func (s *Server) apiSetMilestones(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	var in []milestoneJSON
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	ms := make([]board.Milestone, 0, len(in))
	for _, m := range in {
		bm := board.Milestone{Name: m.Name}
		if m.Date != "" {
			var d toml.LocalDate
			if err := d.UnmarshalText([]byte(m.Date)); err != nil {
				return nil, badRequest("milestone %q: date %q is not YYYY-MM-DD", m.Name, m.Date)
			}
			bm.Date = &d
		}
		ms = append(ms, bm)
	}
	if err := p.SetMilestones(ms); err != nil {
		return nil, badRequest("%v", err)
	}
	return nil, s.Index.RefreshProject(p.Slug())
}

type milestoneSummary struct {
	Name     string         `json:"name"` // "" for tasks without a milestone
	Date     string         `json:"date,omitempty"`
	Defined  bool           `json:"defined"` // listed in project.toml (false: only used by tasks)
	Counts   map[string]int `json:"counts"`  // by status
	Total    int            `json:"total"`
	Tokens   int64          `json:"tokens"`
	Seconds  int64          `json:"seconds"`
	Self     bool           `json:"partly_self_reported"`
	Overdue  bool           `json:"overdue"`
	OpenTask []string       `json:"open_tasks"`
}

// apiMilestoneSummary returns each milestone with task counts by status and spent totals, plus the tasks
// that have no milestone (the "unscheduled" group).
func (s *Server) apiMilestoneSummary(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	rows, err := s.Index.Find(filter.Query{}, index.FindOptions{Project: p.Slug(), IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	byName := map[string]*milestoneSummary{}
	var order []string
	add := func(name string, defined bool, date string) *milestoneSummary {
		k := strings.ToLower(name)
		if m, ok := byName[k]; ok {
			return m
		}
		m := &milestoneSummary{Name: name, Defined: defined, Date: date, Counts: map[string]int{}, OpenTask: []string{}}
		byName[k] = m
		order = append(order, k)
		return m
	}
	today := s.Board.Now().In(s.Board.Location()).Format("2006-01-02") // dates are days in the board's time zone
	for _, m := range p.Config().Milestones {
		d := ""
		if m.Date != nil {
			d = m.Date.String()
		}
		add(m.Name, true, d)
	}
	for _, row := range rows {
		m := add(row.Milestone, false, "")
		m.Counts[row.Status]++
		m.Total++
		m.Tokens += row.SpentTokens
		m.Seconds += int64(row.SpentTime / time.Second)
		m.Self = m.Self || row.SpentSelf > 0
		if row.Status != string(board.Done) && !row.Archived {
			m.OpenTask = append(m.OpenTask, row.ID)
		}
	}
	out := make([]*milestoneSummary, 0, len(order))
	for _, k := range order {
		m := byName[k]
		m.Overdue = m.Date != "" && m.Date < today && len(m.OpenTask) > 0
		out = append(out, m)
	}
	return out, nil
}

func (s *Server) apiReleases(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	rels, err := p.Releases()
	if err != nil {
		return nil, err
	}
	if rels == nil {
		rels = []board.Release{}
	}
	return rels, nil
}

func (s *Server) apiRecordRelease(r *http.Request) (any, error) {
	p, err := s.projectArg(r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	var in struct {
		ID       string `json:"id"`
		Target   string `json:"target"`
		Revision string `json:"revision"`
		Notes    string `json:"notes"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	rel, err := p.RecordRelease(board.Release{ID: in.ID, Target: in.Target, Revision: in.Revision, Notes: in.Notes})
	if err != nil {
		return nil, badRequest("%v", err)
	}
	return rel, s.Index.RefreshReleases(p.Slug())
}

// apiAdvance moves an item task to its next pipeline step (or the step named in "to").
func (s *Server) apiAdvance(r *http.Request) (any, error) {
	p, id, err := s.projectOfID(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var in struct {
		To   string `json:"to"`
		From string `json:"from"` // the step the caller saw; refused with 409 if the task is elsewhere now
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			return nil, err
		}
	}
	t, err := p.Get(id)
	if err != nil {
		return nil, err
	}
	if in.From != "" && (t.Pipeline == nil || !strings.EqualFold(t.Pipeline.At, in.From)) {
		return nil, apiError{http.StatusConflict, "the pipeline step changed meanwhile; reload the task"}
	}
	if err != nil {
		return nil, err
	}
	pl, next, err := s.MCP.NextStep(p, t, in.To)
	if err != nil {
		return nil, badRequest("%v", err)
	}
	st, _ := s.suiteOf(p)
	from := t.Pipeline.At
	note := fmt.Sprintf("%s %s: %s → %s", st.Words.Item, pl.Name, from, next)
	if _, err := p.UpdateIf(id, s.person, board.Edit{Pipeline: &board.Pipeline{Name: pl.Name, At: next}}, note, nil, func(cur *board.Task) error {
		if cur.Pipeline == nil || cur.Pipeline.At != from {
			return apiError{http.StatusConflict, "the pipeline step changed meanwhile; reload the task"}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	s.refreshTask(p, id)
	return map[string]string{"at": next}, nil
}

// apiFeedback files a feedback report from the UI or the CLI (authorized like every /api call).
func (s *Server) apiFeedback(r *http.Request) (any, error) {
	var in mcpserver.FeedbackIn
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFeedbackBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, badRequest("bad request body: %v", err)
	}
	via := strings.TrimSpace(in.Via)
	if via == "" || len(via) > 40 {
		via = "the web UI"
	}
	return s.fileFeedback(in, s.person, via)
}

// maxFeedbackBody allows a 10 MB screenshot in base64 plus text.
const maxFeedbackBody = 15 << 20

func (s *Server) fileFeedback(in mcpserver.FeedbackIn, by board.Actor, via string) (any, error) {
	p, err := s.feedbackProject(in.Project)
	if err != nil {
		return nil, err
	}
	t, note, err := s.MCP.FileFeedback(p, in, by, via)
	if err != nil && t == nil {
		return nil, badRequest("%v", err)
	}
	st, _ := s.suiteOf(p)
	out := map[string]string{"id": t.ID, "project": p.Slug(), "kind": st.Words.Feedback}
	var warn []string
	for _, w := range []string{note, errString(err)} {
		if w != "" {
			warn = append(warn, w)
		}
	}
	if len(warn) > 0 {
		out["warning"] = strings.Join(warn, "; ") // filed, with something the reporter should know
	}
	return out, nil
}

// feedbackProject picks the project for a report: the one named, else [intake] project, else the only one.
func (s *Server) feedbackProject(name string) (*board.Project, error) {
	if name == "" {
		name = s.intake.Project
	}
	if name != "" {
		return s.projectArg(name)
	}
	ps, err := s.Board.Projects()
	if err != nil {
		return nil, err
	}
	if len(ps) == 1 {
		return ps[0], nil
	}
	return nil, badRequest("say which project the report is for (project), or set [intake] project in board.toml")
}

// ---- the opt-in intake listener (spec §8.2) ----

// IntakeSettings are board.toml's [intake] keys.
type IntakeSettings struct {
	Listen    string `toml:"listen"`     // e.g. "0.0.0.0:47322"; empty: no listener
	Token     string `toml:"token"`      // Bearer token reporters send; required when listen is set
	Project   string `toml:"project"`    // default project for reports that name none
	PerMinute int    `toml:"per_minute"` // reports per client IP per minute; default 20
}

// startIntake opens the second listener, which serves only POST /feedback.
func (s *Server) startIntake() error {
	in := s.intake
	if in.Listen == "" {
		return nil
	}
	if len(in.Token) < 16 {
		return errors.New("board.toml [intake]: set token to a secret of at least 16 characters before enabling listen")
	}
	if in.PerMinute <= 0 {
		in.PerMinute = 20
	}
	ln, err := net.Listen("tcp", in.Listen)
	if err != nil {
		return fmt.Errorf("board.toml [intake] listen %s: %w", in.Listen, err)
	}
	limiter := newRateLimiter(in.PerMinute, time.Minute)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /feedback", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !limiter.allow(ip) {
			http.Error(w, "too many reports; try again in a minute", http.StatusTooManyRequests)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+in.Token)) != 1 {
			http.Error(w, "missing or wrong intake token", http.StatusUnauthorized)
			return
		}
		var fb mcpserver.FeedbackIn
		dec := json.NewDecoder(io.LimitReader(r.Body, maxFeedbackBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&fb); err != nil {
			http.Error(w, "bad request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// With [intake] project set, the listener files only there (§8.2): reporters cannot pick another project.
		if in.Project != "" {
			if fb.Project != "" && !s.sameProject(fb.Project, in.Project) {
				http.Error(w, "this listener files reports only into "+in.Project, http.StatusForbidden)
				return
			}
			fb.Project = in.Project
		}
		fb.Reporter = board.CleanName(fb.Reporter, 40)
		fb.Via = ""
		res, err := s.fileFeedback(fb, s.intakeActor(fb), "the intake listener from "+ip)
		if err != nil {
			status := http.StatusBadRequest
			var ae apiError
			switch {
			case errors.As(err, &ae):
				status = ae.status
			case errors.Is(err, board.ErrNotFound):
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, res)
	})
	s.intakeSrv = &http.Server{Handler: s.track(mux), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute}
	s.intakeAddr = ln.Addr().String()
	go s.intakeSrv.Serve(ln)
	s.logf("feedback intake listening on %s", s.intakeAddr)
	return nil
}

// intakeActor is who a report from the listener is recorded as: never a person or an agent of this board, but
// "<feedback word>:<reporter>" with kind "intake" (e.g. playtest:mika), so a remote reporter cannot pass for one.
func (s *Server) intakeActor(fb mcpserver.FeedbackIn) board.Actor {
	word := "feedback"
	if p, err := s.feedbackProject(fb.Project); err == nil {
		if st, _ := s.suiteOf(p); st != nil {
			if f := strings.Fields(st.Words.Feedback); len(f) > 0 {
				word = strings.ToLower(f[0])
			}
		}
	}
	name := fb.Reporter
	if name == "" {
		name = "anonymous"
	}
	return board.Actor{Kind: "intake", Name: word + ":" + name}
}

// sameProject reports whether two project names (folder or key) name the same project.
func (s *Server) sameProject(a, b string) bool {
	pa, err1 := s.projectArg(a)
	pb, err2 := s.projectArg(b)
	return err1 == nil && err2 == nil && pa == pb
}

// IntakeAddr returns the intake listener's address, or "" if it is off.
func (s *Server) IntakeAddr() string { return s.intakeAddr }

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: map[string][]time.Time{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.limit {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	if len(l.hits) > 10000 { // forget old clients
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	return true
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
