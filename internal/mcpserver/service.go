// Package mcpserver implements the board's agent interface: the MCP tools, the per-session state behind
// them (claims, usage reporting, the update digest), and intake of measured usage from client transcripts.
// Service holds the logic and is transport-free; tools.go puts it on an MCP server.
package mcpserver

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RoyalNoob/quest-map/internal/adapter"
	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/filter"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/suite"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// Instructions are sent to clients on initialize.
const Instructions = `quest-map is the task board for this game project.
Pick up work with task_claim; it marks the task as yours. Record progress with task_note as you go.
When the work is ready for a person to check – or you cannot continue – move it to review with task_edit and leave a note saying what is done and how to pick it up again. Put the PR or Lore revision in url.
Do not change tasks whose doer is person (notes are fine), and do not change done tasks.
Tell the board which model you are with model on your first write. Unless a reply says your usage is measured, also pass spent_tokens and spent_time for the work since your last report – a rough estimate is fine.
Replies may end with "Updates:" – changes others made to the task you are working on. Read them.
Agents do not set status done unless the board allows it; move finished work to review.
Before exploring the code to answer a question, try kb_find: the knowledge base keeps answers earlier agents found, and says when one may be out of date. After an answer that took real exploring, save it with kb_write (sources: the files and tasks you checked).
The last line of each reply ("quest-map session: …") lets the board measure your usage; ignore it.`

// Caller identifies who is calling: one agent session.
type Caller struct {
	ID            string // the session's name on the board, e.g. "claude-code:7f3a2c"; used for claims
	Client        string // client name/version from MCP initialize, e.g. "claude-code/2.4.1"
	ClientSession string // the client's own session id (Claude Code: CLAUDE_CODE_SESSION_ID), if known
	Cwd           string // the agent's working folder, if known
}

// Service is the board's agent interface.
type Service struct {
	b      *board.Board
	ix     *index.Index
	vcs    *vcs.Detector
	suites *suite.Live
	now    func() time.Time

	lockCache *adapter.LockCache
	dirs      workdirs // per project, the last working folder an agent used (for the web UI's lock state)

	mu       sync.Mutex
	sessions map[string]*session // by Caller.ID
	byClient map[string]string   // client session id → Caller.ID
	shortIDs map[string]string   // MCP session id → short id, for clients that connect without the proxy

	// Usage intake (usage.go). cursorMu guards store and ingestMu; each client session's transcripts are read
	// under that session's own lock, never under mu.
	cursorMu sync.Mutex
	store    *cursorStore
	ingestMu map[string]*sync.Mutex
}

type session struct {
	caller        Caller
	started       time.Time
	model         string
	measured      bool
	measuredTold  bool
	cursors       map[string]string      // task id → last activity record id this session has seen
	taskMu        map[string]*sync.Mutex // task id → lock making a cursor's read and advance one step
	lastReport    map[string]time.Time   // task id → when this session last reported usage on it
	ended         map[string]time.Time   // task id → when this session's own write ended its claim on it
	lastIngest    time.Time              // when measured usage was last recorded for this session
	announceSeen  string                 // last announcement record id shown
	announceSince time.Time
}

// New returns a service over a board and its index.
func New(b *board.Board, ix *index.Index, det *vcs.Detector) *Service {
	if det == nil {
		det = vcs.NewDetector()
	}
	return &Service{b: b, ix: ix, vcs: det, suites: suite.NewLive(b.Root()), now: time.Now, lockCache: adapter.NewLockCache(),
		dirs: workdirs{path: filepath.Join(b.Root(), ".run", "workdirs.json")}, sessions: map[string]*session{}, byClient: map[string]string{}}
}

// session returns the state for a caller, creating it on first use.
func (s *Service) session(c Caller) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[c.ID]
	if ss == nil {
		ss = &session{caller: c, started: s.now(), cursors: map[string]string{}, taskMu: map[string]*sync.Mutex{},
			lastReport: map[string]time.Time{}, ended: map[string]time.Time{}, announceSince: s.now()}
		s.sessions[c.ID] = ss
	}
	if c.ClientSession != "" {
		s.byClient[c.ClientSession] = c.ID
		ss.caller.ClientSession = c.ClientSession
	}
	if c.Cwd != "" {
		ss.caller.Cwd = c.Cwd
	}
	if c.Client != "" && (ss.caller.Client == "" || strings.Contains(c.Client, "/")) {
		ss.caller.Client = c.Client
	}
	return ss
}

// taskLock returns the lock for a session's cursor on one task.
func (s *Service) taskLock(ss *session, id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := ss.taskMu[id]
	if m == nil {
		m = &sync.Mutex{}
		ss.taskMu[id] = m
	}
	return m
}

func (s *Service) actor(c Caller) board.Actor {
	ss := s.session(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	return board.Actor{Kind: "agent", Session: c.ID, Client: c.Client, Model: ss.model}
}

// errorf makes a tool error the agent can act on.
func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// notAllowed is a board-rule refusal: it reads as its message alone and matches board.ErrNotAllowed.
func notAllowed(format string, args ...any) error {
	return ruleError{msg: fmt.Sprintf(format, args...)}
}

type ruleError struct{ msg string }

func (e ruleError) Error() string { return e.msg }
func (e ruleError) Unwrap() error { return board.ErrNotAllowed }

// ---- projects ----

// project resolves a project argument (folder name or key, any case), or the project linked to the
// caller's working folder.
func (s *Service) project(c Caller, arg string) (*board.Project, error) {
	ps, err := s.b.Projects()
	if err != nil {
		return nil, err
	}
	if arg = strings.TrimSpace(arg); arg != "" {
		for _, p := range ps {
			if strings.EqualFold(p.Slug(), arg) || strings.EqualFold(p.Key(), arg) {
				return p, nil
			}
		}
		return nil, errorf("no project %q; project_list shows the projects", arg)
	}
	if c.Cwd == "" {
		return nil, errorf("pass project: this client did not say which folder it works in")
	}
	keys := s.vcs.Keys(c.Cwd)
	var slugs []string
	for _, k := range keys {
		slug, err := s.ix.ProjectForRemote(k)
		if err != nil {
			return nil, err
		}
		if slug != "" && !slices.Contains(slugs, slug) {
			slugs = append(slugs, slug)
		}
	}
	switch {
	case len(slugs) == 1:
		s.dirs.set(slugs[0], c.Cwd)
		return s.b.Project(slugs[0])
	case len(slugs) > 1:
		return nil, errorf("pass project: this folder's remotes are linked to several projects (%s)", strings.Join(slugs, ", "))
	}
	if len(keys) == 0 {
		return nil, errorf("pass project: %s is not in a git repository or Lore workspace quest-map knows", c.Cwd)
	}
	return nil, errorf("pass project, or link this repository with repo_link (remote %s is not linked to a project)", keys[0])
}

// projectOfID returns the project whose key prefixes a task id.
func (s *Service) projectOfID(id string) (*board.Project, string, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	i := strings.LastIndex(id, "-")
	if i <= 0 {
		return nil, id, errorf("%q is not a task id like RPG-12", id)
	}
	ps, err := s.b.Projects()
	if err != nil {
		return nil, id, err
	}
	for _, p := range ps {
		if p.Key() == id[:i] {
			return p, id, nil
		}
	}
	return nil, id, errorf("no project has the key %s", id[:i])
}

// ---- replies ----

// TaskOut is a task in structured replies.
type TaskOut struct {
	ID         string            `json:"id"`
	Project    string            `json:"project"`
	Title      string            `json:"title"`
	Status     string            `json:"status"`
	Doer       string            `json:"doer"`
	Assignees  []string          `json:"assignees,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"` // custom fields (suite)
	Priority   string            `json:"priority,omitempty"`
	Discipline string            `json:"discipline,omitempty"`
	Milestone  string            `json:"milestone,omitempty"`
	Labels     []string          `json:"labels,omitempty"`
	Asset      string            `json:"asset,omitempty"`
	BlockedBy  []string          `json:"blocked_by,omitempty"`
	ClaimBy    string            `json:"claim_by,omitempty"`
	URL        string            `json:"url,omitempty"`
	Spent      *SpentOut         `json:"spent,omitempty"`
}

// SpentOut is a task's usage totals.
type SpentOut struct {
	Tokens  int64  `json:"tokens"`
	Time    string `json:"time"`
	Seconds int64  `json:"seconds"`
	Partly  bool   `json:"partly_self_reported,omitempty"`
}

func taskOut(p *board.Project, t *board.Task, sp *board.Spent) TaskOut {
	o := TaskOut{ID: t.ID, Project: p.Slug(), Title: t.Title, Status: string(t.Status), Doer: string(t.Doer), Assignees: t.Assignees, Fields: t.Custom,
		Priority: string(t.Priority), Discipline: t.Discipline, Milestone: t.Milestone, Labels: t.Labels,
		Asset: t.Asset, BlockedBy: t.BlockedBy, URL: t.URL}
	if t.Claim != nil {
		o.ClaimBy = t.Claim.By
	}
	if sp != nil && sp.Records > 0 {
		o.Spent = &SpentOut{Tokens: sp.Tokens.Sum(), Time: board.FormatDuration(sp.Time), Seconds: int64(sp.Time / time.Second), Partly: sp.Self > 0}
	}
	return o
}

func rowOut(r index.Row) TaskOut {
	o := TaskOut{ID: r.ID, Project: r.Project, Title: r.Title, Status: r.Status, Doer: r.Doer, Assignees: r.Assignees, Fields: r.Custom, Priority: r.Priority,
		Discipline: r.Discipline, Milestone: r.Milestone, Labels: r.Labels, Asset: r.Asset, BlockedBy: r.BlockedBy, ClaimBy: r.ClaimBy}
	if r.SpentRecords > 0 {
		o.Spent = &SpentOut{Tokens: r.SpentTokens, Time: board.FormatDuration(r.SpentTime), Seconds: int64(r.SpentTime / time.Second), Partly: r.SpentSelf > 0}
	}
	return o
}

// line renders a task in one line: "RPG-12 doing (agent, code, vertical-slice): Title · spent 42k tok, 25m".
func line(o TaskOut) string {
	meta := []string{o.Doer}
	for _, a := range o.Assignees {
		meta = append(meta, "@"+a)
	}
	for _, v := range []string{o.Discipline, o.Milestone} {
		if v != "" {
			meta = append(meta, v)
		}
	}
	if o.Priority != "" && o.Priority != string(board.Normal) {
		meta = append(meta, o.Priority)
	}
	s := fmt.Sprintf("%s %s (%s): %s", o.ID, o.Status, strings.Join(meta, ", "), o.Title)
	if o.ClaimBy != "" {
		s += " [claimed by " + o.ClaimBy + "]"
	}
	if o.Spent != nil {
		s += " · spent " + spentText(o.Spent)
	}
	return s
}

func spentText(sp *SpentOut) string {
	var parts []string
	if sp.Tokens != 0 {
		parts = append(parts, board.FormatTokens(sp.Tokens)+" tok")
	}
	if sp.Seconds != 0 {
		parts = append(parts, sp.Time)
	}
	if len(parts) == 0 {
		return "nothing yet"
	}
	s := strings.Join(parts, ", ")
	if sp.Partly {
		s = "~" + s
	}
	return s
}

// ---- usage ----

// UsageArgs are the optional usage fields of write tools.
type UsageArgs struct {
	Model       string `json:"model,omitempty" jsonschema:"your model, e.g. claude-opus-5-5; remembered for this session"`
	SpentTokens int64  `json:"spent_tokens,omitempty" jsonschema:"recommended: tokens used on this task since your last report (estimate)"`
	SpentTime   string `json:"spent_time,omitempty" jsonschema:"recommended: working time since your last report, like 25m or 1h30m"`
}

// usage turns usage args into a report, remembering the model. Measured sessions' self-reports are dropped,
// with a note the first time.
func (s *Service) usage(c Caller, a UsageArgs) (*board.Usage, string, error) {
	model := strings.TrimSpace(a.Model)
	if model != "" && !modelRe.MatchString(model) {
		return nil, "", errorf("model %q: use the model id, e.g. claude-opus-5-5 (letters, digits and . _ - : / @ [ ], at most 80)", a.Model)
	}
	if a.SpentTokens < 0 {
		return nil, "", errorf("spent_tokens cannot be negative")
	}
	var d time.Duration
	if a.SpentTime != "" {
		var err error
		if d, err = board.ParseDuration(a.SpentTime); err != nil {
			return nil, "", errorf("spent_time: %v", err)
		}
		if d < 0 {
			return nil, "", errorf("spent_time cannot be negative")
		}
	}
	ss := s.session(c)
	s.mu.Lock()
	if model != "" {
		ss.model = model
	}
	measured, told := ss.measured, ss.measuredTold
	model = ss.model
	if measured && (a.SpentTokens != 0 || d != 0) {
		ss.measuredTold = true
	}
	s.mu.Unlock()
	if a.SpentTokens == 0 && d == 0 {
		return nil, "", nil
	}
	if measured {
		if !told {
			return nil, "Your usage is measured from the client's own logs, so spent_tokens / spent_time were ignored; you no longer need to pass them.", nil
		}
		return nil, "", nil
	}
	return &board.Usage{Tokens: board.Tokens{Total: a.SpentTokens}, Time: d, Source: board.SourceSelf, Model: model}, "", nil
}

var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,79}$`)

// tip returns the reminder for a session that held the task's claim (before the write) and wrote without
// reporting usage, or "".
func (s *Service) tip(c Caller, t *board.Task, claim *board.Claim, reported bool) string {
	if claim == nil || claim.By != c.ID {
		return ""
	}
	ss := s.session(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if reported {
		ss.lastReport[t.ID] = now
		return ""
	}
	if ss.measured {
		return ""
	}
	since := claim.At
	if r, ok := ss.lastReport[t.ID]; ok && r.After(since) {
		since = r
	}
	return fmt.Sprintf("Tip: pass model / spent_tokens / spent_time. %s since your last report.", board.FormatDuration(now.Sub(since)))
}

// ---- reads ----

// openFilter adds -status:done unless the filter already says something about status.
func openFilter(f string, fields []string) (filter.Query, error) {
	q, err := filter.ParseFields(f, fields)
	if err != nil {
		return q, err
	}
	if !q.Has("status") {
		q.Terms = append([]filter.Term{{Neg: true, Key: "status", Value: string(board.Done)}}, q.Terms...)
	}
	return q, nil
}

// sortForClaim orders candidates: priority (urgent first), milestone date (earliest first, none last), created.
func (s *Service) sortForClaim(p *board.Project, rows []index.Row) {
	date := func(r index.Row) (time.Time, bool) { return p.MilestoneDate(r.Milestone) }
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ra, rb := claimRank(a.Priority), claimRank(b.Priority); ra != rb {
			return ra > rb
		}
		da, oka := date(a)
		db, okb := date(b)
		if oka != okb {
			return oka
		}
		if oka && !da.Equal(db) {
			return da.Before(db)
		}
		return a.Created.Before(b.Created)
	})
}

// claimRank ranks a priority for claiming; an empty or unknown priority counts as normal.
func claimRank(p string) int {
	if r := board.Priority(p).Rank(); r > 0 {
		return r
	}
	return board.Normal.Rank()
}

// refresh updates the index right after a write, so the watcher's event for it is an echo.
func (s *Service) refresh(p *board.Project, t *board.Task) {
	s.ix.RefreshAfterWrite(p.TaskPath(t.ID, t.Archived))
}

func (s *Service) spent(p *board.Project, id string) *board.Spent {
	sp, err := p.Spent(id)
	if err != nil {
		return nil
	}
	return &sp
}

// checkAgentMayChange enforces the board rules for agents: no changes to done tasks, and only notes on
// person tasks.
func checkAgentMayChange(t *board.Task, noteOnly bool) error {
	switch {
	case t.Archived:
		return notAllowed("%s is archived", t.ID)
	case t.Status == board.Done:
		return notAllowed("%s is done; agents do not change done tasks", t.ID)
	case t.Doer == board.Person && !noteOnly:
		return notAllowed("%s is a person's task; agents may only add notes to it", t.ID)
	}
	return nil
}

// agentCheck is the board-rule check for an agent write, run under the project lock. noteOnly: the write
// only adds a note (allowed on person tasks and on tasks others have claimed). stale: a claim of another
// session that was found stale just before the write; only that exact claim may be overridden.
func agentCheck(c Caller, noteOnly bool, stale *board.Claim) func(*board.Task) error {
	return func(t *board.Task) error {
		if err := checkAgentMayChange(t, noteOnly); err != nil {
			return err
		}
		if !noteOnly && t.Claim != nil && t.Claim.By != c.ID && !sameClaim(stale, t.Claim) {
			return notAllowed("%s is claimed by %s and the claim is not stale; add a note with task_note instead", t.ID, t.Claim.By)
		}
		return nil
	}
}

func sameClaim(a, b *board.Claim) bool {
	return a != nil && b != nil && a.By == b.By && a.At.Equal(b.At)
}

// staleClaim returns t's claim if another session holds it and it is stale (index stale:yes), else nil.
func (s *Service) staleClaim(c Caller, p *board.Project, t *board.Task) (*board.Claim, error) {
	if t.Claim == nil || t.Claim.By == c.ID {
		return nil, nil
	}
	rows, err := s.ix.FindString("stale:yes", index.FindOptions{Project: p.Slug()})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.ID == t.ID {
			cl := *t.Claim
			return &cl, nil
		}
	}
	return nil, nil
}

// join puts reply parts together, skipping empty ones.
func join(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), "\n")
}

var errNoMatch = errors.New("no match")
