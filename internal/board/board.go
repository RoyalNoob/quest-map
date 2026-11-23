package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ErrNotFound is returned for a task or project that does not exist.
var ErrNotFound = errors.New("not found")

// Lines quest-map keeps in the board root's .gitattributes and .gitignore, for boards synced with git (§3.5).
const (
	gitattributesHeader = "# quest-map: activity files are append-only, so a union merge is safe.\n"
	gitattributesLine   = "activity.jsonl merge=union"
	gitignoreHeader     = "# quest-map: machine-local state and the derived index are never synced.\n"
)

var gitignoreLines = []string{".run/", ".cache/", TrashDir + "/"}

var (
	slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	keyRe  = regexp.MustCompile(`^[A-Z][A-Z0-9]*$`)
)

// Board is a data folder holding projects.
type Board struct {
	root string
	// Now returns the current time; tests replace it.
	Now func() time.Time

	cfg    BoardConfig
	loc    *time.Location // log-line time zone, from cfg.Timezone
	origin string         // id of this board copy (.run/origin)

	mu       sync.Mutex
	projects map[string]*Project
}

// Open opens the board at root, creating its folders, board.toml, .gitattributes, .gitignore and this
// copy's origin id if needed.
func Open(root string) (*Board, error) {
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		return nil, err
	}
	cfg, loc, err := loadBoardConfig(root)
	if err != nil {
		return nil, err
	}
	if err := ensureLines(filepath.Join(root, ".gitattributes"), gitattributesHeader, gitattributesLine); err != nil {
		return nil, err
	}
	if err := ensureLines(filepath.Join(root, ".gitignore"), gitignoreHeader, gitignoreLines...); err != nil {
		return nil, err
	}
	origin, err := loadOrigin(root)
	if err != nil {
		return nil, err
	}
	return &Board{root: root, Now: time.Now, cfg: cfg, loc: loc, origin: origin, projects: map[string]*Project{}}, nil
}

// Forget drops a project from the board's cache, after its folder was removed or moved away.
func (b *Board) Forget(slug string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.projects, slug)
}

// Root returns the board folder.
func (b *Board) Root() string { return b.root }

// Config returns the board settings, with built-in defaults filled in.
func (b *Board) Config() BoardConfig { return b.cfg }

func (b *Board) now() time.Time { return b.Now().UTC().Truncate(time.Second) }

// Milestone is a named target with an optional date.
type Milestone struct {
	Name string          `toml:"name"`
	Date *toml.LocalDate `toml:"date,omitempty"`
}

// ProjectConfig is a project's project.toml.
type ProjectConfig struct {
	Key        string      `toml:"key"`
	Name       string      `toml:"name"`
	NextID     int         `toml:"next_id"`
	Suite      string      `toml:"suite,omitempty"`
	Repos      []string    `toml:"repos,omitempty"`
	Milestones []Milestone `toml:"milestones,omitempty"`
	// ArchiveDoneAfterDays archives done tasks that have not changed for this many days; 0 never does.
	ArchiveDoneAfterDays int `toml:"archive_done_after_days,omitempty"`
}

// CreateProject adds a project folder. slug names the folder (lowercase), key prefixes task ids (uppercase).
func (b *Board) CreateProject(slug, key, name string) (*Project, error) {
	if !slugRe.MatchString(slug) {
		return nil, fmt.Errorf("project folder %q: use lowercase letters, digits and dashes", slug)
	}
	if !keyRe.MatchString(key) {
		return nil, fmt.Errorf("project key %q: use uppercase letters and digits, starting with a letter", key)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	dir := filepath.Join(b.root, "projects", slug)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("project %q already exists", slug)
	}
	existing, err := b.projectsLocked()
	if err != nil {
		return nil, err
	}
	for _, p := range existing {
		if p.Key() == key {
			return nil, fmt.Errorf("project key %q is already used by %q", key, p.slug)
		}
	}
	for _, d := range []string{"tasks", "archive"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return nil, err
		}
	}
	if name == "" {
		name = slug
	}
	p := &Project{board: b, slug: slug, dir: dir}
	p.setCfg(ProjectConfig{Key: key, Name: name, NextID: 1})
	if err := p.saveConfig(); err != nil {
		return nil, err
	}
	b.projects[slug] = p
	return p, nil
}

// Project returns the project in folder slug.
func (b *Board) Project(slug string) (*Project, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.projectLocked(slug)
}

func (b *Board) projectLocked(slug string) (*Project, error) {
	if p, ok := b.projects[slug]; ok {
		return p, nil
	}
	dir := filepath.Join(b.root, "projects", slug)
	data, err := os.ReadFile(filepath.Join(dir, "project.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("project %q: %w", slug, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var cfg ProjectConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("project %q: project.toml: %w", slug, err)
	}
	if !keyRe.MatchString(cfg.Key) {
		return nil, fmt.Errorf("project %q: project.toml: invalid key %q", slug, cfg.Key)
	}
	p := &Project{board: b, slug: slug, dir: dir}
	p.setCfg(cfg)
	b.projects[slug] = p
	return p, nil
}

// Projects lists all projects, sorted by folder name.
func (b *Board) Projects() ([]*Project, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.projectsLocked()
}

func (b *Board) projectsLocked() ([]*Project, error) {
	entries, err := os.ReadDir(filepath.Join(b.root, "projects"))
	if err != nil {
		return nil, err
	}
	var out []*Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(b.root, "projects", e.Name(), "project.toml")); err != nil {
			continue
		}
		p, err := b.projectLocked(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].slug < out[j].slug })
	return out, nil
}

// Move moves a task to another project. It gets a new id there, moved_from is set to the old id, any claim
// is cleared (a doing task goes back to todo), and its attachments move with it. Tasks that waited on the old
// id wait on the new one (RetargetBlockers).
func (b *Board) Move(src *Project, id string, dst *Project, by Actor) (*Task, error) {
	t, err := b.MoveIf(src, id, dst, by, nil)
	if t == nil {
		return nil, err
	}
	_, rerr := b.RetargetBlockers(id, t.ID, by)
	return t, errors.Join(err, rerr)
}

// MoveIf is Move with a check run on the task under both project locks, so rules about the task (who holds
// its claim, whether it is done) are checked against the state the move acts on. A check error stops the move.
// It does not rewrite blocked_by in other tasks; call RetargetBlockers after it.
func (b *Board) MoveIf(src *Project, id string, dst *Project, by Actor, check func(*Task) error) (*Task, error) {
	if src == dst {
		return nil, errors.New("task is already in that project")
	}
	first, second := src, dst
	if second.slug < first.slug {
		first, second = second, first
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	second.mu.Lock()
	defer second.mu.Unlock()

	t, err := src.readLocked(id)
	if err != nil {
		return nil, err
	}
	if t.Archived {
		return nil, fmt.Errorf("%s is archived", id)
	}
	if check != nil {
		if err := check(t); err != nil {
			return nil, err
		}
	}
	newID, err := dst.allocIDLocked()
	if err != nil {
		return nil, err
	}
	now := b.now()
	t.ID, t.MovedFrom = newID, id
	t.Claim = nil
	t.appendLog(now, by.who(), "", "moved from "+id)
	statusFrom := Status("")
	if t.Status == Doing {
		statusFrom, t.Status = Doing, Todo
		t.appendLog(now, by.who(), "", fmt.Sprintf("status %s → %s", Doing, Todo))
	}
	if err := dst.writeLocked(t); err != nil {
		return nil, err
	}
	if err := removeWithRetry(src.taskPath(id, false)); err != nil {
		// Never leave the task in both projects: take the new copy back out.
		os.Remove(dst.taskPath(newID, false))
		return nil, err
	}
	var attErr error
	if att := filepath.Join(src.dir, "attachments", id); dirExists(att) {
		attErr = os.MkdirAll(filepath.Join(dst.dir, "attachments"), 0o755)
		if attErr == nil {
			attErr = renameWithRetry(att, filepath.Join(dst.dir, "attachments", newID))
		}
		if attErr != nil {
			attErr = fmt.Errorf("%s moved to %s, but its attachments are still in %s: %w", id, newID, att, attErr)
		}
	}
	// The move records link the two ids (To on the old one, From on the new one), so spent totals can follow
	// a task across projects.
	if err := b.appendActivity(src.activityPath(), by.record(now, KindEdit, id, func(r *Record) {
		r.Fields, r.To, r.Text = []string{"moved"}, newID, "moved to "+newID
	})); err != nil {
		return nil, err
	}
	err = b.appendActivity(dst.activityPath(),
		by.record(now, KindEdit, newID, func(r *Record) {
			r.Fields, r.From, r.Text = []string{"moved_from"}, id, "moved from "+id
		}),
		by.record(now, KindStatus, newID, func(r *Record) { r.From, r.To = string(statusFrom), string(t.Status) }))
	return t, errors.Join(err, attErr)
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
