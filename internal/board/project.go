package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Actor is who makes a change: an agent session or a person.
type Actor struct {
	Kind    string // "agent", "person", or "intake" (a report from the feedback listener)
	Name    string // shown in log lines; defaults to Session, then Kind
	Session string
	Client  string
	Model   string
}

func (a Actor) who() string {
	switch {
	case a.Name != "":
		return a.Name
	case a.Session != "":
		return a.Session
	case a.Kind != "":
		return a.Kind
	}
	return "unknown"
}

func (a Actor) record(at time.Time, kind, task string, fill func(r *Record)) Record {
	r := Record{ID: newID(at), At: at, Kind: kind, Actor: a.Kind, Session: a.Session, Client: a.Client, Model: a.Model}
	if task != "" {
		r.Task = &task
	}
	if fill != nil {
		fill(&r)
	}
	return r
}

func (a Actor) usageRecord(at time.Time, task string, u *Usage) Record {
	return a.record(at, KindUsage, task, func(r *Record) {
		r.Source = u.Source
		if r.Source == "" {
			r.Source = SourceSelf
		}
		if u.Model != "" {
			r.Model = u.Model
		}
		if !u.Tokens.IsZero() {
			tok := u.Tokens
			r.Tokens = &tok
		}
		r.TimeS = int64(u.Time.Round(time.Second) / time.Second)
		r.CostUSD = u.CostUSD
	})
}

// Project is one project folder. Its methods are safe for concurrent use within one process; all writes to
// a project are serialized.
type Project struct {
	board *Board
	slug  string
	dir   string

	mu  sync.Mutex
	cfg ProjectConfig
	// key mirrors cfg.Key so Key can be read without p.mu (callers may hold another project's lock).
	key atomic.Pointer[string]
}

// setCfg replaces the project's settings. p.mu is held, or p is not shared yet.
func (p *Project) setCfg(cfg ProjectConfig) {
	p.cfg = cfg
	k := cfg.Key
	p.key.Store(&k)
}

// Slug returns the project's folder name.
func (p *Project) Slug() string { return p.slug }

// Key returns the project's task id prefix.
func (p *Project) Key() string {
	if k := p.key.Load(); k != nil {
		return *k
	}
	return ""
}

// Config returns a copy of the project's settings.
func (p *Project) Config() ProjectConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.cfg
	c.Repos = slices.Clone(c.Repos)
	c.Milestones = slices.Clone(c.Milestones)
	return c
}

// Dir returns the project folder.
func (p *Project) Dir() string { return p.dir }

func (p *Project) taskPath(id string, archived bool) string {
	sub := "tasks"
	if archived {
		sub = "archive"
	}
	return filepath.Join(p.dir, sub, id+".md")
}

func (p *Project) activityPath() string { return filepath.Join(p.dir, "activity.jsonl") }

func (p *Project) saveConfig() error {
	data, err := toml.Marshal(p.cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(p.dir, "project.toml"), data, 0o644)
}

// idNumber returns n for "KEY-n", or 0.
func (p *Project) idNumber(id string) int {
	rest, ok := strings.CutPrefix(id, p.cfg.Key+"-")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// allocIDLocked returns the next free id and advances next_id. Existing files are checked too, so a stale
// next_id (for example after syncing the board between machines) never reuses an id.
func (p *Project) allocIDLocked() (string, error) {
	n := max(p.cfg.NextID, 1)
	for _, sub := range []string{"tasks", "archive"} {
		entries, err := os.ReadDir(filepath.Join(p.dir, sub))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		for _, e := range entries {
			if k := p.idNumber(strings.TrimSuffix(e.Name(), ".md")); k >= n {
				n = k + 1
			}
		}
	}
	p.cfg.NextID = n + 1
	if err := p.saveConfig(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d", p.cfg.Key, n), nil
}

func (p *Project) readLocked(id string) (*Task, error) {
	for _, archived := range []bool{false, true} {
		path := p.taskPath(id, archived)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		t, err := ParseIn(data, p.board.loc)
		if err != nil {
			var pe *ParseError
			if errors.As(err, &pe) {
				pe.Path = path
			}
			return nil, err
		}
		t.Archived = archived
		return t, nil
	}
	return nil, fmt.Errorf("task %s: %w", id, ErrNotFound)
}

func (p *Project) writeLocked(t *Task) error {
	data, err := t.Render()
	if err != nil {
		return err
	}
	return writeFileAtomic(p.taskPath(t.ID, t.Archived), data, 0o644)
}

// Get reads a task from the tasks or archive folder. For a task that was moved away, the not-found error
// names its new id.
func (p *Project) Get(id string) (*Task, error) {
	p.mu.Lock()
	t, err := p.readLocked(id)
	p.mu.Unlock()
	return t, p.board.movedHint(id, err)
}

// List reads all open (not archived) tasks, sorted by id number. Files that fail to parse are returned as
// errors next to the tasks that did.
func (p *Project) List() ([]*Task, []error) { return p.list(false) }

// ListArchived reads all archived tasks.
func (p *Project) ListArchived() ([]*Task, []error) { return p.list(true) }

func (p *Project) list(archived bool) ([]*Task, []error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listLocked(archived)
}

func (p *Project) listLocked(archived bool) ([]*Task, []error) {
	dir := filepath.Dir(p.taskPath("x", archived))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{err}
	}
	var tasks []*Task
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		t, err := p.readLocked(strings.TrimSuffix(name, ".md"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tasks = append(tasks, t)
	}
	slices.SortFunc(tasks, func(a, b *Task) int { return p.idNumber(a.ID) - p.idNumber(b.ID) })
	return tasks, errs
}

// Create adds a task. ID and Created are set here; an empty Status, Doer or Priority gets the board's default
// ([defaults] in board.toml; built in: todo, person, normal). The creation is logged with the optional usage
// report.
func (p *Project) Create(t *Task, by Actor, u *Usage) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if strings.TrimSpace(t.Title) == "" {
		return nil, errors.New("title is empty")
	}
	d := p.board.cfg.Defaults
	if t.Status == "" {
		t.Status = d.Status
	}
	if t.Doer == "" {
		t.Doer = d.Doer
	}
	if t.Priority == "" {
		t.Priority = d.Priority
	}
	t.ID = "x" // placeholder so validation can run before an id is spent
	if err := t.validate(); err != nil {
		return nil, err
	}
	id, err := p.allocIDLocked()
	if err != nil {
		return nil, err
	}
	now := p.board.now()
	t.ID, t.Created, t.Archived, t.doc, t.loc = id, now, false, nil, p.board.loc
	t.Body = strings.Trim(t.Body, "\n")
	t.bodyRaw, t.logRaw = "", ""
	if _, err := t.Render(); err != nil { // sets up body and log sections
		return nil, err
	}
	t.appendLog(now, by.who(), u.Suffix(), "created")
	if err := p.writeLocked(t); err != nil {
		return nil, err
	}
	recs := []Record{by.record(now, KindStatus, id, func(r *Record) { r.To = string(t.Status) })}
	if !u.IsZero() {
		recs = append(recs, by.usageRecord(now, id, u))
	}
	return t, p.board.appendActivity(p.activityPath(), recs...)
}

// Edit lists front-matter changes. Nil fields are left alone; an empty string clears a field. Pipeline with
// an empty Name clears the pipeline.
type Edit struct {
	Title, Discipline, Milestone, Asset, FoundIn, FixedIn, URL *string
	Status                                                     *Status
	Doer                                                       *Doer
	Priority                                                   *Priority
	Labels, BlockedBy, Assignees                               *[]string
	Pipeline                                                   *Pipeline
	Body                                                       *string
	// Custom sets custom front-matter keys (spec §4.1); "" removes one. Built-in keys are refused.
	Custom map[string]string
	// AddLabels/RemoveLabels and AddAssignees/RemoveAssignees change single entries of those lists, so an edit
	// from a stale copy of the task cannot drop entries someone else added meanwhile. They apply after
	// Labels/Assignees. Names compare without case.
	AddLabels, RemoveLabels, AddAssignees, RemoveAssignees []string
}

// customKeyRe is the shape of a custom field key (as suites declare them).
var customKeyRe = regexp.MustCompile(`^[a-z][a-z_]*$`)

// IsCustomKey reports whether k can be a custom field key: the right shape and not a built-in key.
func IsCustomKey(k string) bool {
	return customKeyRe.MatchString(k) && !slices.Contains(KnownKeys(), k)
}

// editList applies adds and removes to a list (case-insensitive, order kept) and reports whether it changed.
func editList(list []string, add, remove []string, clean func(string) string) ([]string, bool) {
	out := slices.Clone(list)
	changed := false
	for _, r := range remove {
		r = clean(r)
		if i := slices.IndexFunc(out, func(x string) bool { return strings.EqualFold(x, r) }); r != "" && i >= 0 {
			out = slices.Delete(out, i, i+1)
			changed = true
		}
	}
	for _, a := range add {
		a = clean(a)
		if a != "" && !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, a) }) {
			out = append(out, a)
			changed = true
		}
	}
	return out, changed
}

// apply changes t and returns the names of the changed fields (status not included) and whether the status
// changed.
func (e Edit) apply(t *Task) (changed []string, statusFrom Status, err error) {
	setStr := func(name string, dst *string, v *string) {
		if v != nil && *dst != *v {
			*dst = *v
			changed = append(changed, name)
		}
	}
	if e.Title != nil && strings.TrimSpace(*e.Title) == "" {
		return nil, "", errors.New("title is empty")
	}
	setStr("title", &t.Title, e.Title)
	setStr("discipline", &t.Discipline, e.Discipline)
	setStr("milestone", &t.Milestone, e.Milestone)
	setStr("asset", &t.Asset, e.Asset)
	setStr("found_in", &t.FoundIn, e.FoundIn)
	setStr("fixed_in", &t.FixedIn, e.FixedIn)
	setStr("url", &t.URL, e.URL)
	if e.Doer != nil && *e.Doer != t.Doer {
		if !e.Doer.Valid() {
			return nil, "", fmt.Errorf("doer %q is not person or agent", *e.Doer)
		}
		t.Doer = *e.Doer
		changed = append(changed, "doer")
	}
	if e.Priority != nil && *e.Priority != t.Priority {
		if !e.Priority.Valid() {
			return nil, "", fmt.Errorf("priority %q is not low, normal, high or urgent", *e.Priority)
		}
		t.Priority = *e.Priority
		changed = append(changed, "priority")
	}
	if e.Labels != nil && !slices.Equal(*e.Labels, t.Labels) {
		t.Labels = slices.Clone(*e.Labels)
		changed = append(changed, "labels")
	}
	if e.Assignees != nil {
		if a := CleanNames(*e.Assignees); !slices.Equal(a, t.Assignees) {
			t.Assignees = a
			changed = append(changed, "assignees")
		}
	}
	if e.BlockedBy != nil && !slices.Equal(*e.BlockedBy, t.BlockedBy) {
		t.BlockedBy = slices.Clone(*e.BlockedBy)
		changed = append(changed, "blocked_by")
	}
	if e.Pipeline != nil {
		var want *Pipeline
		if e.Pipeline.Name != "" {
			p := *e.Pipeline
			want = &p
		}
		if (want == nil) != (t.Pipeline == nil) || (want != nil && *want != *t.Pipeline) {
			t.Pipeline = want
			changed = append(changed, "pipeline")
		}
	}
	if len(e.AddLabels)+len(e.RemoveLabels) > 0 {
		if l, ok := editList(t.Labels, e.AddLabels, e.RemoveLabels, strings.TrimSpace); ok {
			t.Labels = l
			if !slices.Contains(changed, "labels") {
				changed = append(changed, "labels")
			}
		}
	}
	if len(e.AddAssignees)+len(e.RemoveAssignees) > 0 {
		if a, ok := editList(t.Assignees, e.AddAssignees, e.RemoveAssignees, func(s string) string { return CleanName(s, 80) }); ok {
			t.Assignees = a
			if !slices.Contains(changed, "assignees") {
				changed = append(changed, "assignees")
			}
		}
	}
	for _, k := range sortedKeys(e.Custom) {
		if !IsCustomKey(k) {
			return nil, "", fmt.Errorf("field %q: not a custom field key", k)
		}
		v := CleanName(e.Custom[k], 200)
		if cur := t.Custom[k]; cur == v {
			continue
		}
		if v == "" {
			delete(t.Custom, k)
		} else {
			if t.Custom == nil {
				t.Custom = map[string]string{}
			}
			t.Custom[k] = v
		}
		changed = append(changed, k)
	}
	if e.Body != nil && strings.Trim(*e.Body, "\n") != t.Body {
		t.Body = strings.Trim(*e.Body, "\n")
		changed = append(changed, "body")
	}
	if e.Status != nil && *e.Status != t.Status {
		if !e.Status.Valid() {
			return nil, "", fmt.Errorf("status %q is not one of backlog, todo, doing, review, done", *e.Status)
		}
		statusFrom = t.Status
		t.Status = *e.Status
		if t.Status != Doing && t.Claim != nil {
			t.Claim = nil
			changed = append(changed, "claim")
		}
	}
	return changed, statusFrom, nil
}

// Update applies an edit, adds an optional note and usage report, and records the change in the log and
// the activity file. Moving a task out of doing clears its claim.
func (p *Project) Update(id string, by Actor, e Edit, note string, u *Usage) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, err := p.readLocked(id)
	if err != nil {
		return nil, err
	}
	if t.Archived {
		return nil, fmt.Errorf("%s is archived", id)
	}
	changed, from, err := e.apply(t)
	if err != nil {
		return nil, err
	}
	return t, p.commitLocked(t, by, changed, from, note, u)
}

// Note appends a log line with an optional usage report.
func (p *Project) Note(id string, by Actor, text string, u *Usage) (*Task, error) {
	if strings.TrimSpace(text) == "" && u.IsZero() {
		return nil, errors.New("note is empty")
	}
	return p.Update(id, by, Edit{}, text, u)
}

// Claim marks the task as being worked by the actor's session: status doing, claim set. It fails if the
// task is done, archived, or claimed by another session. Choosing which task to claim is up to the caller.
func (p *Project) Claim(id string, by Actor) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, err := p.readLocked(id)
	if err != nil {
		return nil, err
	}
	session := by.Session
	if session == "" {
		session = by.who()
	}
	switch {
	case t.Archived || t.Status == Done:
		return nil, fmt.Errorf("%s is done", id)
	case t.Claim != nil && t.Claim.By != session:
		return nil, fmt.Errorf("%s is claimed by %s", id, t.Claim.By)
	}
	from := Status("")
	if t.Status != Doing {
		from = t.Status
		t.Status = Doing
	}
	t.Claim = &Claim{By: session, At: p.board.now()}
	return t, p.commitLocked(t, by, []string{"claim"}, from, "claimed", nil)
}

// Release clears the claim and sets the task back to todo, with a note.
func (p *Project) Release(id string, by Actor, note string, u *Usage) (*Task, error) {
	if strings.TrimSpace(note) == "" {
		note = "released"
	}
	todo := Todo
	return p.Update(id, by, Edit{Status: &todo}, note, u)
}

// commitLocked writes t and records what changed.
func (p *Project) commitLocked(t *Task, by Actor, changed []string, from Status, note string, u *Usage) error {
	statusChanged := from != ""
	if len(changed) == 0 && !statusChanged && strings.TrimSpace(note) == "" && u.IsZero() {
		return nil
	}
	now := p.board.now()
	suffix := u.Suffix()
	if statusChanged {
		s := ""
		if strings.TrimSpace(note) == "" {
			s = suffix
		}
		t.appendLog(now, by.who(), s, fmt.Sprintf("status %s → %s", from, t.Status))
	}
	switch {
	case strings.TrimSpace(note) != "":
		t.appendLog(now, by.who(), suffix, note)
	case !statusChanged && !u.IsZero():
		t.appendLog(now, by.who(), suffix, "reported usage")
	}
	if err := p.writeLocked(t); err != nil {
		return err
	}
	var recs []Record
	if len(changed) > 0 {
		recs = append(recs, by.record(now, KindEdit, t.ID, func(r *Record) { r.Fields = changed }))
	}
	if statusChanged {
		recs = append(recs, by.record(now, KindStatus, t.ID, func(r *Record) { r.From, r.To = string(from), string(t.Status) }))
	}
	if n := strings.TrimSpace(note); n != "" {
		n = noteRecordText(n)
		recs = append(recs, by.record(now, KindNote, t.ID, func(r *Record) { r.Text = n }))
	}
	if !u.IsZero() {
		recs = append(recs, by.usageRecord(now, t.ID, u))
	}
	return p.board.appendActivity(p.activityPath(), recs...)
}

// RecordUsage stores a usage report. task may be "" for usage not tied to a task (project overhead). It
// writes no log line when task is "".
func (p *Project) RecordUsage(task string, by Actor, u *Usage) error {
	if u.IsZero() {
		return nil
	}
	if task != "" {
		_, err := p.Note(task, by, "", u)
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.board.appendActivity(p.activityPath(), by.usageRecord(p.board.now(), "", u))
}

// Archive moves a done task to the archive folder.
func (p *Project) Archive(id string, by Actor) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, err := p.readLocked(id)
	if err != nil {
		return err
	}
	if t.Archived {
		return nil
	}
	if t.Status != Done {
		return fmt.Errorf("%s is %s; only done tasks can be archived", id, t.Status)
	}
	return p.archiveLocked(t, by, p.board.now())
}

// ArchiveDoneFor archives a done task that has not changed for at least age (auto-archive). The time the task
// last changed is the later of its last log line and its last activity record, both of which travel with the
// board, so it does not depend on this machine. The archive is stamped with that time plus age, not the
// current time: two machines syncing one board by git then write the same archive file, and the merge is
// clean. It reports whether the task was archived.
func (p *Project) ArchiveDoneFor(id string, by Actor, age time.Duration) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, err := p.readLocked(id)
	if err != nil || t.Archived || t.Status != Done {
		return false, err
	}
	last := time.Time{}
	if n := len(t.Log); n > 0 {
		last = t.Log[n-1].Time
	}
	recs, _, err := readRecords(p.activityPath())
	if err != nil {
		return false, err
	}
	for _, r := range recs {
		if r.TaskID() == id && r.At.After(last) {
			last = r.At // hand edits (a status set to done in an editor) leave only an activity record
		}
	}
	if last.IsZero() || p.board.now().Before(last.Add(age)) {
		return false, nil
	}
	return true, p.archiveLocked(t, by, last.Add(age).UTC().Truncate(time.Minute))
}

func (p *Project) archiveLocked(t *Task, by Actor, now time.Time) error {
	id := t.ID
	t.appendLog(now, by.who(), "", "archived")
	t.Archived = true
	if err := p.writeLocked(t); err != nil {
		return err
	}
	if err := removeWithRetry(p.taskPath(id, false)); err != nil {
		// Never leave the task in both folders: take the archive copy back out.
		os.Remove(p.taskPath(id, true))
		return err
	}
	return p.board.appendActivity(p.activityPath(), by.record(now, KindEdit, id, func(r *Record) { r.Fields = []string{"archived"} }))
}

// Activity reads the project's activity records in time order, plus the number of unreadable lines.
func (p *Project) Activity() ([]Record, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return readRecords(p.activityPath())
}

// maxMoveHops bounds how many links (moves, renumbers) are followed, in case they form a loop.
const maxMoveHops = 32

// Spent totals the usage recorded for a task, following it through moves and renumbers (see Board.Ledger).
func (p *Project) Spent(id string) (Spent, error) {
	l, err := p.board.Ledger()
	if err != nil {
		return Spent{}, err
	}
	return l.Spent[id], nil
}
