package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/filter"
	"github.com/RoyalNoob/quest-map/internal/index"
)

// ---- project tools ----

// ProjectOut is a project in structured replies.
type ProjectOut struct {
	Slug    string   `json:"slug"`
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Repos   []string `json:"repos,omitempty"`
	Open    int      `json:"open"`
	Current bool     `json:"current,omitempty"`
}

// ProjectsOut is the reply of project_list.
type ProjectsOut struct {
	Projects []ProjectOut `json:"projects"`
}

// ProjectList lists projects with open task counts and marks the one for the caller's folder.
func (s *Service) ProjectList(c Caller) (string, ProjectsOut, error) {
	ps, err := s.b.Projects()
	if err != nil {
		return "", ProjectsOut{}, err
	}
	cur, _ := s.project(c, "")
	out := ProjectsOut{Projects: []ProjectOut{}}
	var lines []string
	for _, p := range ps {
		rows, err := s.ix.Find(filter.Query{Terms: []filter.Term{{Neg: true, Key: "status", Value: "done"}}}, index.FindOptions{Project: p.Slug()})
		if err != nil {
			return "", out, err
		}
		cfg := p.Config()
		o := ProjectOut{Slug: p.Slug(), Key: p.Key(), Name: cfg.Name, Repos: cfg.Repos, Open: len(rows), Current: cur == p}
		out.Projects = append(out.Projects, o)
		l := fmt.Sprintf("%s (%s) %q: %d open", o.Slug, o.Key, o.Name, o.Open)
		if o.Current {
			l += " ← this folder"
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		return "No projects yet. Create one with project_create.", out, nil
	}
	return strings.Join(lines, "\n"), out, nil
}

// ProjectCreateIn is the input of project_create.
type ProjectCreateIn struct {
	Key  string `json:"key" jsonschema:"task id prefix, uppercase letters and digits, e.g. RPG"`
	Name string `json:"name,omitempty" jsonschema:"display name"`
	Slug string `json:"slug,omitempty" jsonschema:"folder name, lowercase; default: the key in lowercase"`
	Repo string `json:"repo,omitempty" jsonschema:"git remote URL or remote key to link, e.g. git@github.com:acme/rpg.git"`
}

// ProjectCreate adds a project, optionally linked to a repository.
func (s *Service) ProjectCreate(c Caller, in ProjectCreateIn) (string, ProjectOut, error) {
	key := strings.ToUpper(strings.TrimSpace(in.Key))
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = strings.ToLower(key)
	}
	p, err := s.b.CreateProject(slug, key, in.Name)
	if err != nil {
		return "", ProjectOut{}, err
	}
	if in.Repo != "" {
		if _, _, err := s.RepoLink(c, RepoLinkIn{Project: slug, Remote: in.Repo}); err != nil {
			return "", ProjectOut{}, fmt.Errorf("project %s created, but linking the repo failed: %w", slug, err)
		}
	}
	cfg := p.Config()
	o := ProjectOut{Slug: p.Slug(), Key: p.Key(), Name: cfg.Name, Repos: cfg.Repos}
	text := fmt.Sprintf("Created project %s (%s).", o.Slug, o.Key)
	if len(o.Repos) > 0 {
		text += " Linked " + strings.Join(o.Repos, ", ") + "."
	}
	return text, o, nil
}

// RepoLinkIn is the input of repo_link and repo_unlink.
type RepoLinkIn struct {
	Project string `json:"project,omitempty" jsonschema:"project folder or key (repo_link only); default: none, required for repo_link"`
	Remote  string `json:"remote,omitempty" jsonschema:"git remote URL (any form) or remote key such as git:github.com/acme/rpg; repo_link default: the remote of your working folder"`
}

// RepoOut is the reply of repo_link / repo_unlink.
type RepoOut struct {
	Project string `json:"project"`
	Remote  string `json:"remote"`
}

// RepoLink maps a remote to a project.
func (s *Service) RepoLink(c Caller, in RepoLinkIn) (string, RepoOut, error) {
	if strings.TrimSpace(in.Project) == "" {
		return "", RepoOut{}, errorf("pass project: which project should %s belong to?", in.Remote)
	}
	p, err := s.project(c, in.Project)
	if err != nil {
		return "", RepoOut{}, err
	}
	if strings.TrimSpace(in.Remote) == "" {
		if c.Cwd == "" {
			return "", RepoOut{}, errorf("pass remote: this client did not say which folder it works in")
		}
		keys := s.vcs.Keys(c.Cwd)
		if len(keys) == 0 {
			return "", RepoOut{}, errorf("pass remote: %s is not in a git repository or Lore workspace with a remote", c.Cwd)
		}
		in.Remote = keys[0]
	}
	key, ok := s.vcs.NormalizeKey(in.Remote)
	if !ok {
		return "", RepoOut{}, errorf("%q is not a git or Lore remote URL or remote key", in.Remote)
	}
	if other, err := s.ix.ProjectForRemote(key); err != nil {
		return "", RepoOut{}, err
	} else if other != "" && other != p.Slug() {
		return "", RepoOut{}, errorf("%s is already linked to project %s; repo_unlink it first", key, other)
	}
	if err := p.LinkRepo(key); err != nil {
		return "", RepoOut{}, err
	}
	if err := s.ix.RefreshProject(p.Slug()); err != nil {
		return "", RepoOut{}, err
	}
	return fmt.Sprintf("Linked %s to project %s.", key, p.Slug()), RepoOut{Project: p.Slug(), Remote: key}, nil
}

// RepoUnlink removes a remote mapping.
func (s *Service) RepoUnlink(c Caller, in RepoLinkIn) (string, RepoOut, error) {
	key, ok := s.vcs.NormalizeKey(in.Remote)
	if !ok {
		return "", RepoOut{}, errorf("%q is not a git remote URL or remote key", in.Remote)
	}
	slug, err := s.ix.ProjectForRemote(key)
	if err != nil {
		return "", RepoOut{}, err
	}
	if slug == "" {
		return fmt.Sprintf("%s was not linked to a project.", key), RepoOut{Remote: key}, nil
	}
	p, err := s.b.Project(slug)
	if err != nil {
		return "", RepoOut{}, err
	}
	if _, err := p.UnlinkRepo(key); err != nil {
		return "", RepoOut{}, err
	}
	if err := s.ix.RefreshProject(slug); err != nil {
		return "", RepoOut{}, err
	}
	return fmt.Sprintf("Unlinked %s from project %s.", key, slug), RepoOut{Project: slug, Remote: key}, nil
}

// ---- task reads ----

// FindIn is the input of task_find.
type FindIn struct {
	Project string `json:"project,omitempty" jsonschema:"project folder or key; default: the project linked to your working folder"`
	Filter  string `json:"filter,omitempty" jsonschema:"filter such as 'status:todo discipline:art label:physics bridge'; without a status term, done tasks are left out"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of tasks; default 50"`
}

// TasksOut is a list of tasks.
type TasksOut struct {
	Tasks []TaskOut `json:"tasks"`
	More  int       `json:"more,omitempty"`
	notes string    // the reply lines that are not in the fields above (briefMessage)
}

func (o TasksOut) briefMessage() string { return o.notes }

// Find lists tasks matching a filter.
func (s *Service) Find(c Caller, in FindIn) (string, TasksOut, error) {
	p, err := s.project(c, in.Project)
	if err != nil {
		return "", TasksOut{}, err
	}
	q, err := openFilter(in.Filter, s.fieldKeys(p))
	if err != nil {
		return "", TasksOut{}, err
	}
	rows, err := s.ix.Find(q, index.FindOptions{Project: p.Slug()})
	if err != nil {
		return "", TasksOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	out := TasksOut{Tasks: []TaskOut{}}
	var lines []string
	for i, r := range rows {
		if i == limit {
			out.More = len(rows) - limit
			lines = append(lines, fmt.Sprintf("+%d more; narrow the filter or raise limit", out.More))
			break
		}
		o := rowOut(r)
		out.Tasks = append(out.Tasks, o)
		l := line(o)
		if r.ParseError != "" {
			l += " [file cannot be read: " + r.ParseError + "]"
		}
		lines = append(lines, l)
	}
	text := strings.Join(lines, "\n")
	var notes []string
	if len(rows) == 0 {
		text = fmt.Sprintf("No tasks in %s match %q.", p.Slug(), q.String())
		notes = append(notes, text)
	} else if out.More > 0 {
		notes = append(notes, lines[len(lines)-1])
	}
	ann := s.announcements(c, p)
	out.notes = join(append(notes, ann)...)
	return join(text, ann), out, nil
}

// ShowIn is the input of task_show.
type ShowIn struct {
	ID string `json:"id" jsonschema:"task id, e.g. RPG-12"`
}

// TaskDetail is the reply of task_show.
type TaskDetail struct {
	TaskOut
	Body string `json:"body,omitempty"`
	// Knowledge lists the knowledge base pages that name this task as a source (§4.5).
	Knowledge []string `json:"knowledge,omitempty"`
	Log       []string `json:"log,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	// Files are the repo files the task names, in the caller's working copy (git or Lore): what its asset maps
	// to by the suite's asset resolver (§4.2), then file paths in backticks in the body that exist there.
	// Empty when the caller is not in a working copy or the mapping is not certain.
	Files []string `json:"files,omitempty"`
	// Locks is the read-only lock state of Files (§5); nil when there are no files.
	Locks *LockState `json:"locks,omitempty"`
	notes string     // the reply lines that are not in the fields above (briefMessage)
}

func (d TaskDetail) briefMessage() string { return d.notes }

// Show returns a task in full.
func (s *Service) Show(c Caller, in ShowIn) (string, TaskDetail, error) {
	p, id, err := s.projectOfID(in.ID)
	if err != nil {
		return "", TaskDetail{}, err
	}
	var warnings []string
	if probs, err := s.ix.ParseProblems(); err == nil {
		for _, pr := range probs {
			if pr.ID == id {
				warnings = append(warnings, "the task file cannot be read ("+pr.Err+"); this is its last good version")
			}
		}
	}
	t, err := p.Get(id)
	var pe *board.ParseError
	if errors.As(err, &pe) {
		rows, ferr := s.ix.Find(filter.Query{}, index.FindOptions{Project: p.Slug(), IncludeArchived: true})
		if ferr == nil {
			for _, r := range rows {
				if r.ID == id {
					d := TaskDetail{TaskOut: rowOut(r), Warnings: append(warnings, "file error: "+pe.Err.Error())}
					return join(line(d.TaskOut), "Warning: the task file cannot be read: "+pe.Err.Error()), d, nil
				}
			}
		}
		return "", TaskDetail{}, err
	}
	if err != nil {
		return "", TaskDetail{}, err
	}
	s.rememberWorkdir(c, p)
	root, files := s.taskFiles(c.Cwd, p, t.Asset, t.Body)
	d := TaskDetail{TaskOut: taskOut(p, t, s.spent(p, id)), Body: t.Body, Warnings: warnings, Files: files}
	if len(files) > 0 {
		ls := s.lockState(context.Background(), p, c.Cwd, root, files)
		d.Locks = &ls
	}
	var b strings.Builder
	b.WriteString(line(d.TaskOut) + "\n")
	for _, kv := range [][2]string{{"labels", strings.Join(t.Labels, ", ")}, {"asset", t.Asset}, {"files", strings.Join(d.Files, ", ")}, {"blocked by", strings.Join(t.BlockedBy, ", ")},
		{"found in", t.FoundIn}, {"fixed in", t.FixedIn}, {"url", t.URL}, {"moved from", t.MovedFrom}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
		}
	}
	if d.Locks != nil {
		for _, l := range lockLines(*d.Locks) {
			b.WriteString(l + "\n")
		}
	}
	if t.Pipeline != nil {
		fmt.Fprintf(&b, "pipeline: %s, at %s\n", t.Pipeline.Name, t.Pipeline.At)
	}
	for _, k := range slices.Sorted(maps.Keys(t.Custom)) {
		fmt.Fprintf(&b, "%s: %s\n", k, t.Custom[k])
	}
	if t.Body != "" {
		b.WriteString("\n" + t.Body + "\n")
	}
	if len(t.Log) > 0 {
		b.WriteString("\nLog:\n")
		start := max(0, len(t.Log)-30)
		if start > 0 {
			fmt.Fprintf(&b, "(%d earlier entries in the file)\n", start)
		}
		for _, e := range t.Log[start:] {
			who := e.Who
			if e.Usage != "" {
				who += " (" + e.Usage + ")"
			}
			l := fmt.Sprintf("%s %s: %s", e.Time.In(s.b.Location()).Format("2006-01-02 15:04"), who, e.Text)
			d.Log = append(d.Log, l)
			b.WriteString("- " + l + "\n")
		}
	}
	if pages, err := s.ix.PagesForTask(id); err == nil && len(pages) > 0 {
		b.WriteString("\nKnowledge base pages about this task:\n")
		for _, pg := range pages {
			d.Knowledge = append(d.Knowledge, pg.Project+"/"+pg.Slug)
			fmt.Fprintf(&b, "- %s/%s: %s\n", pg.Project, pg.Slug, pg.Title)
		}
	}
	for _, w := range warnings {
		b.WriteString("Warning: " + w + "\n")
	}
	d.notes = s.digest(c, p, t)
	return join(strings.TrimRight(b.String(), "\n"), d.notes), d, nil
}

// ---- claiming ----

// ClaimIn is the input of task_claim.
type ClaimIn struct {
	Project    string `json:"project,omitempty" jsonschema:"project folder or key; default: the project linked to your working folder"`
	Discipline string `json:"discipline,omitempty" jsonschema:"only claim tasks of this discipline, e.g. art"`
	Milestone  string `json:"milestone,omitempty" jsonschema:"only claim tasks of this milestone"`
	Filter     string `json:"filter,omitempty" jsonschema:"extra filter terms, e.g. 'label:physics'"`
}

// ClaimOut is the reply of task_claim.
type ClaimOut struct {
	Claimed *TaskOut `json:"claimed,omitempty"`
}

// Claim picks the best unblocked todo agent task and claims it in one check-and-set.
func (s *Service) Claim(c Caller, in ClaimIn) (string, ClaimOut, error) {
	p, err := s.project(c, in.Project)
	if err != nil {
		return "", ClaimOut{}, err
	}
	q, err := filter.ParseFields(in.Filter, s.fieldKeys(p))
	if err != nil {
		return "", ClaimOut{}, err
	}
	q.Terms = append(q.Terms, filter.Term{Key: "status", Value: "todo"}, filter.Term{Key: "doer", Value: "agent"},
		filter.Term{Key: "blocked", Value: "no"})
	if in.Discipline != "" {
		q.Terms = append(q.Terms, filter.Term{Key: "discipline", Value: in.Discipline})
	}
	if in.Milestone != "" {
		q.Terms = append(q.Terms, filter.Term{Key: "milestone", Value: in.Milestone})
	}
	rows, err := s.ix.Find(q, index.FindOptions{Project: p.Slug()})
	if err != nil {
		return "", ClaimOut{}, err
	}
	s.sortForClaim(p, rows)
	by := s.actor(c)
	for _, r := range rows {
		t, err := p.ClaimUnblockedIf(r.ID, by, func(t *board.Task) error {
			if t.Status != board.Todo || t.Doer != board.Agent || t.Claim != nil {
				return errNoMatch // changed since the index saw it; try the next one
			}
			return nil
		})
		var pe *board.ParseError
		switch {
		case errors.Is(err, errNoMatch), errors.Is(err, board.ErrClaimLost), errors.Is(err, board.ErrNotFound),
			errors.Is(err, os.ErrNotExist), errors.As(err, &pe):
			continue // taken, changed, gone or unreadable since the index saw it
		case err != nil:
			return "", ClaimOut{}, fmt.Errorf("claim %s: %w", r.ID, err)
		}
		s.refresh(p, t)
		s.markSeen(c, p, t.ID)
		o := taskOut(p, t, s.spent(p, t.ID))
		text := "Claimed " + line(o)
		if t.Body != "" {
			text += "\n\n" + t.Body
		}
		if o.Spent != nil {
			text += "\n\nAlready spent on this task: " + spentText(o.Spent) + "."
		}
		return join(text, s.announcements(c, p)), ClaimOut{Claimed: &o}, nil
	}
	return join(fmt.Sprintf("No unblocked todo agent task in %s matches %q.", p.Slug(), q.String()), s.announcements(c, p)), ClaimOut{}, nil
}

// ---- writes ----

// NewIn is the input of task_new.
type NewIn struct {
	Project    string            `json:"project,omitempty" jsonschema:"project folder or key; default: the project linked to your working folder"`
	Title      string            `json:"title"`
	Body       string            `json:"body,omitempty" jsonschema:"Markdown description"`
	Status     string            `json:"status,omitempty" jsonschema:"backlog or todo (default from board.toml)"`
	Doer       string            `json:"doer,omitempty" jsonschema:"agent or person (default from board.toml)"`
	Assignees  []string          `json:"assignees,omitempty" jsonschema:"people the task is assigned to (names)"`
	Priority   string            `json:"priority,omitempty" jsonschema:"low, normal, high or urgent"`
	Discipline string            `json:"discipline,omitempty" jsonschema:"e.g. code, blueprint, art, anim, audio, qa"`
	Milestone  string            `json:"milestone,omitempty"`
	Labels     []string          `json:"labels,omitempty"`
	Asset      string            `json:"asset,omitempty" jsonschema:"the item the task is about (game dev: asset name or Unreal object path, e.g. /Game/Props/SM_Bridge_Mid)"`
	Pipeline   string            `json:"pipeline,omitempty" jsonschema:"pipeline the item follows (game dev: prop, character, level, vfx); starts at its first step"`
	BlockedBy  []string          `json:"blocked_by,omitempty" jsonschema:"ids of tasks this one waits on"`
	Fields     map[string]string `json:"fields,omitempty" jsonschema:"custom fields of the project's suite, e.g. {\"platform\": \"Win64\"} (game dev: platform, ue_map)"`
	Template   string            `json:"template,omitempty" jsonschema:"suite task template that fills the fields you leave empty and adds its labels (game dev: Bug, Crash, Performance, Prop asset, Character asset, Level)"`
	UsageArgs
}

// applyTemplate fills the fields a new task leaves empty from a suite template and adds its labels.
func (s *Service) applyTemplate(p *board.Project, in *NewIn) error {
	if strings.TrimSpace(in.Template) == "" {
		return nil
	}
	st, _ := s.suiteFor(p)
	if st == nil {
		return errorf("no suite loaded for %s, so no templates", p.Slug())
	}
	tp, ok := st.Template(in.Template)
	if !ok {
		names := st.TemplateNames()
		if len(names) == 0 {
			return errorf("this project's suite has no task templates")
		}
		return errorf("no template %q in this project's suite; templates: %s", in.Template, strings.Join(names, ", "))
	}
	fill := func(dst *string, v string) {
		if strings.TrimSpace(*dst) == "" {
			*dst = v
		}
	}
	fill(&in.Body, tp.Body)
	fill(&in.Status, tp.Status)
	fill(&in.Doer, tp.Doer)
	fill(&in.Priority, tp.Priority)
	fill(&in.Discipline, tp.Discipline)
	fill(&in.Pipeline, tp.Pipeline)
	for _, l := range tp.Labels {
		if !slices.ContainsFunc(in.Labels, func(x string) bool { return strings.EqualFold(x, l) }) {
			in.Labels = append(in.Labels, l)
		}
	}
	return nil
}

// TaskReply is the reply of task writes.
type TaskReply struct {
	Task TaskOut `json:"task"`
}

// New creates a task.
func (s *Service) New(c Caller, in NewIn) (string, TaskReply, error) {
	p, err := s.project(c, in.Project)
	if err != nil {
		return "", TaskReply{}, err
	}
	if err := s.applyTemplate(p, &in); err != nil {
		return "", TaskReply{}, err
	}
	if err := s.checkFields(p, in.Fields); err != nil {
		return "", TaskReply{}, err
	}
	switch board.Status(in.Status) {
	case "", board.Backlog, board.Todo:
	default:
		return "", TaskReply{}, errorf("status: new tasks start as backlog or todo; claim a task with task_claim")
	}
	u, note, err := s.usage(c, in.UsageArgs)
	if err != nil {
		return "", TaskReply{}, err
	}
	blockers, err := s.b.CheckBlockers("", in.BlockedBy)
	if err != nil {
		return "", TaskReply{}, err
	}
	t := &board.Task{Title: in.Title, Body: in.Body, Status: board.Status(in.Status), Doer: board.Doer(in.Doer), Assignees: board.CleanNames(in.Assignees), Custom: board.CleanCustom(in.Fields),
		Priority: board.Priority(in.Priority), Discipline: in.Discipline, Milestone: in.Milestone, Labels: in.Labels,
		Asset: in.Asset, BlockedBy: blockers}
	if in.Pipeline != "" {
		pl, err := s.pipelineStart(p, in.Pipeline)
		if err != nil {
			return "", TaskReply{}, err
		}
		t.Pipeline = pl
	}
	t, err = p.Create(t, s.actor(c), u)
	if err != nil {
		return "", TaskReply{}, err
	}
	s.refresh(p, t)
	s.markSeen(c, p, t.ID)
	o := taskOut(p, t, s.spent(p, t.ID))
	return join("Created "+line(o), note, s.suiteNotes(p, t)), TaskReply{Task: o}, nil
}

// EditIn is the input of task_edit. Only the fields you pass change; "" clears a text field.
type EditIn struct {
	ID         string            `json:"id" jsonschema:"task id, e.g. RPG-12"`
	Status     *string           `json:"status,omitempty" jsonschema:"backlog, todo, doing, review or done; leaving doing clears your claim"`
	Title      *string           `json:"title,omitempty"`
	Body       *string           `json:"body,omitempty"`
	Priority   *string           `json:"priority,omitempty"`
	Doer       *string           `json:"doer,omitempty"`
	Assignees  *[]string         `json:"assignees,omitempty" jsonschema:"people the task is assigned to (replaces the list; [] clears)"`
	Discipline *string           `json:"discipline,omitempty"`
	Milestone  *string           `json:"milestone,omitempty"`
	Labels     *[]string         `json:"labels,omitempty"`
	Asset      *string           `json:"asset,omitempty"`
	BlockedBy  *[]string         `json:"blocked_by,omitempty"`
	Pipeline   *string           `json:"pipeline,omitempty" jsonschema:"set the item's pipeline (starts at its first step; empty removes it); advance steps with pipeline_advance"`
	FoundIn    *string           `json:"found_in,omitempty" jsonschema:"release id the problem was found in (game dev: build id)"`
	FixedIn    *string           `json:"fixed_in,omitempty" jsonschema:"release id the fix is in (game dev: build id)"`
	URL        *string           `json:"url,omitempty" jsonschema:"PR or Lore revision"`
	Fields     map[string]string `json:"fields,omitempty" jsonschema:"custom fields to set; \"\" clears one"`
	Note       string            `json:"note,omitempty" jsonschema:"log line to add with this change"`
	UsageArgs
}

// Edit changes front-matter fields.
func (s *Service) Edit(c Caller, in EditIn) (string, TaskReply, error) {
	p, id, err := s.projectOfID(in.ID)
	if err != nil {
		return "", TaskReply{}, err
	}
	cur, err := p.Get(id)
	if err != nil {
		return "", TaskReply{}, err
	}
	if err := checkAgentMayChange(cur, false); err != nil {
		return "", TaskReply{}, err
	}
	if err := s.checkFields(p, in.Fields); err != nil {
		return "", TaskReply{}, err
	}
	stale, err := s.staleClaim(c, p, cur)
	if err != nil {
		return "", TaskReply{}, err
	}
	e := board.Edit{Title: in.Title, Body: in.Body, Assignees: in.Assignees, Custom: in.Fields, Discipline: in.Discipline, Milestone: in.Milestone, Asset: in.Asset,
		FoundIn: in.FoundIn, FixedIn: in.FixedIn, URL: in.URL, Labels: in.Labels}
	var st board.Status
	if in.Status != nil {
		st = board.Status(strings.ToLower(strings.TrimSpace(*in.Status)))
		if st == board.Done && !s.b.Config().Agents.MaySetDone {
			return "", TaskReply{}, errorf("agents do not set done on this board: move %s to review with a note on what is done and how to check it; a person sets done (board.toml [agents] may_set_done)", id)
		}
		e.Status = &st
	}
	if in.Priority != nil {
		pr := board.Priority(strings.ToLower(*in.Priority))
		e.Priority = &pr
	}
	if in.Doer != nil {
		d := board.Doer(strings.ToLower(*in.Doer))
		e.Doer = &d
	}
	if in.BlockedBy != nil {
		b, err := s.b.CheckBlockers(id, *in.BlockedBy)
		if err != nil {
			return "", TaskReply{}, err
		}
		e.BlockedBy = &b
	}
	if in.Pipeline != nil {
		pl, err := s.pipelineStart(p, *in.Pipeline)
		if err != nil {
			return "", TaskReply{}, err
		}
		e.Pipeline = pl
	}
	base := agentCheck(c, false, stale)
	check := func(t *board.Task) error {
		if err := base(t); err != nil {
			return err
		}
		if st == board.Doing && (t.Claim == nil || t.Claim.By != c.ID) {
			return errorf("use task_claim to start work on a task; status doing needs your claim")
		}
		return nil
	}
	text, out, err := s.write(c, p, id, in.UsageArgs, check, func(by board.Actor, u *board.Usage, check func(*board.Task) error) (*board.Task, error) {
		return p.UpdateIf(id, by, e, in.Note, u, check)
	})
	if err != nil {
		return text, out, err
	}
	if t, gerr := p.Get(id); gerr == nil {
		text = join(text, s.suiteNotes(p, t))
	}
	return text, out, nil
}

// NoteIn is the input of task_note.
type NoteIn struct {
	ID   string `json:"id" jsonschema:"task id, e.g. RPG-12"`
	Text string `json:"text" jsonschema:"what you did, found or decided"`
	UsageArgs
}

// Note appends to the task log.
func (s *Service) Note(c Caller, in NoteIn) (string, TaskReply, error) {
	p, id, err := s.projectOfID(in.ID)
	if err != nil {
		return "", TaskReply{}, err
	}
	return s.write(c, p, id, in.UsageArgs, agentCheck(c, true, nil), func(by board.Actor, u *board.Usage, check func(*board.Task) error) (*board.Task, error) {
		if strings.TrimSpace(in.Text) == "" && u.IsZero() {
			return nil, errorf("note is empty")
		}
		return p.UpdateIf(id, by, board.Edit{}, in.Text, u, check)
	})
}

// ReleaseIn is the input of task_release.
type ReleaseIn struct {
	ID   string `json:"id" jsonschema:"task id, e.g. RPG-12"`
	Note string `json:"note,omitempty" jsonschema:"why, and what someone picking it up should know"`
	UsageArgs
}

// Release clears a claim and sets the task back to todo. Another session's claim can only be released when
// it is stale; a task nobody has claimed has nothing to release.
func (s *Service) Release(c Caller, in ReleaseIn) (string, TaskReply, error) {
	p, id, err := s.projectOfID(in.ID)
	if err != nil {
		return "", TaskReply{}, err
	}
	cur, err := p.Get(id)
	if err != nil {
		return "", TaskReply{}, err
	}
	stale, err := s.staleClaim(c, p, cur)
	if err != nil {
		return "", TaskReply{}, err
	}
	base := agentCheck(c, false, stale)
	check := func(t *board.Task) error {
		if t.Claim == nil {
			return notAllowed("%s is not claimed, so there is nothing to release (to change its status, use task_edit)", t.ID)
		}
		return base(t)
	}
	note := in.Note
	if strings.TrimSpace(note) == "" {
		note = "released"
	}
	todo := board.Todo
	return s.write(c, p, id, in.UsageArgs, check, func(by board.Actor, u *board.Usage, check func(*board.Task) error) (*board.Task, error) {
		return p.UpdateIf(id, by, board.Edit{Status: &todo}, note, u, check)
	})
}

// write runs a task write with usage handling, then builds the reply: the changed-since-claim warning, the
// task line, the usage note or tip, and the update digest. check runs under the project lock with the task
// as it is just before the write; the claim it sees is the one the warning and tip are about.
func (s *Service) write(c Caller, p *board.Project, id string, ua UsageArgs, check func(*board.Task) error,
	do func(board.Actor, *board.Usage, func(*board.Task) error) (*board.Task, error)) (string, TaskReply, error) {
	u, note, err := s.usage(c, ua)
	if err != nil {
		return "", TaskReply{}, err
	}
	var claim *board.Claim
	t, err := do(s.actor(c), u, func(t *board.Task) error {
		if err := check(t); err != nil {
			return err
		}
		if t.Claim != nil {
			cl := *t.Claim
			claim = &cl
		}
		return nil
	})
	if err != nil {
		return "", TaskReply{}, err
	}
	if claim == nil && t.Claim != nil {
		claim = t.Claim
	}
	if claim != nil && claim.By == c.ID && t.Claim == nil {
		ss := s.session(c)
		s.mu.Lock()
		ss.ended[t.ID] = s.now() // usage of this turn can still go to this task (attribute)
		s.mu.Unlock()
	}
	s.refresh(p, t)
	warning, updates := s.updates(c, p, t, claim)
	o := taskOut(p, t, s.spent(p, id))
	tip := ""
	if note == "" {
		tip = s.tip(c, t, claim, u != nil)
	} else {
		s.tip(c, t, claim, true)
	}
	return join(warning, line(o), note, tip, updates), TaskReply{Task: o}, nil
}

// ArchiveIn is the input of task_archive.
type ArchiveIn struct {
	ID string `json:"id" jsonschema:"id of a done task"`
}

// Archive moves a done task to the archive.
func (s *Service) Archive(c Caller, in ArchiveIn) (string, TaskReply, error) {
	p, id, err := s.projectOfID(in.ID)
	if err != nil {
		return "", TaskReply{}, err
	}
	if err := p.Archive(id, s.actor(c)); err != nil {
		return "", TaskReply{}, err
	}
	t, err := p.Get(id)
	if err != nil {
		return "", TaskReply{}, err
	}
	s.ix.RefreshAfterWrite(p.TaskPath(id, false))
	s.refresh(p, t)
	o := taskOut(p, t, nil)
	return "Archived " + line(o), TaskReply{Task: o}, nil
}

// MoveIn is the input of task_move.
type MoveIn struct {
	ID string `json:"id" jsonschema:"task id, e.g. RPG-12"`
	To string `json:"to" jsonschema:"target project folder or key"`
}

// Move moves a task to another project; it gets a new id there.
func (s *Service) Move(c Caller, in MoveIn) (string, TaskReply, error) {
	src, id, err := s.projectOfID(in.ID)
	if err != nil {
		return "", TaskReply{}, err
	}
	cur, err := src.Get(id)
	if err != nil {
		return "", TaskReply{}, err
	}
	stale, err := s.staleClaim(c, src, cur)
	if err != nil {
		return "", TaskReply{}, err
	}
	if err := agentCheck(c, false, stale)(cur); err != nil {
		return "", TaskReply{}, err
	}
	dst, err := s.project(c, in.To)
	if err != nil {
		return "", TaskReply{}, err
	}
	// Checked again under the project locks: the claim or status may have changed since the read above.
	t, err := s.b.MoveIf(src, id, dst, s.actor(c), agentCheck(c, false, stale))
	if err != nil {
		return "", TaskReply{}, err
	}
	s.ix.RefreshAfterWrite(src.TaskPath(id, false))
	re, rerr := s.b.RetargetBlockers(id, t.ID, s.actor(c))
	var waiting []string
	for _, r := range re {
		s.refresh(r.Project, r.Task)
		waiting = append(waiting, r.Task.ID)
	}
	warn := ""
	if rerr != nil {
		warn = fmt.Sprintf("Warning: blocked_by in other tasks still names %s where it could not be updated: %v", id, rerr)
	}
	s.refresh(dst, t)
	s.markSeen(c, dst, t.ID)
	o := taskOut(dst, t, s.spent(dst, t.ID))
	text := fmt.Sprintf("Moved %s to %s as %s", id, dst.Slug(), line(o))
	if len(waiting) > 0 {
		text = join(text, fmt.Sprintf("blocked_by updated to %s in %s.", t.ID, strings.Join(waiting, ", ")))
	}
	text = join(text, warn)
	return text, TaskReply{Task: o}, nil
}
