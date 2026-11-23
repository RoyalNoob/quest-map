package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/RoyalNoob/quest-map/internal/adapter"
	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// LockOut is one locked file of a task (spec §5).
type LockOut struct {
	Path     string `json:"path"`
	Owner    string `json:"owner,omitempty"`
	Since    string `json:"since,omitempty"`
	Provider string `json:"provider"`
}

// LockState is the lock state of a task's files. Checked is false when no lock provider applies (no working
// copy, no files, or a repository that does not use locks). Unknown names each provider that could not be
// asked and why: the lock state is unknown then, which is never an error.
type LockState struct {
	Checked bool      `json:"checked"`
	Locks   []LockOut `json:"locks"`
	Unknown []string  `json:"unknown,omitempty"`
}

// workdirs remembers, per project, the working folder the web UI reads a task's files and locks in (it has no
// working folder of its own): the last folder an agent used the project from, unless a person pinned one in
// Settings. It is kept in .run/workdirs.json – folders are paths on this machine, so it is never synced.
type workdirs struct {
	mu     sync.Mutex
	path   string // "" keeps it in memory only (tests)
	loaded bool
	by     map[string]workdir
}

type workdir struct {
	Dir    string `json:"dir"`
	Pinned bool   `json:"pinned,omitempty"` // set by a person; agents do not change it
}

func (w *workdirs) loadLocked() {
	if w.loaded {
		return
	}
	w.loaded = true
	w.by = map[string]workdir{}
	if w.path == "" {
		return
	}
	if data, err := os.ReadFile(w.path); err == nil {
		json.Unmarshal(data, &w.by)
	}
}

func (w *workdirs) saveLocked() {
	if w.path == "" {
		return
	}
	data, err := json.MarshalIndent(w.by, "", "  ")
	if err != nil {
		return
	}
	tmp := w.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, w.path)
	}
}

// set records an agent's folder for a project, unless a person pinned one.
func (w *workdirs) set(slug, dir string) {
	if dir == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loadLocked()
	if cur := w.by[slug]; cur.Pinned || cur.Dir == dir {
		return
	}
	w.by[slug] = workdir{Dir: dir}
	w.saveLocked()
}

// pin sets a person's folder for a project; "" removes the pin (agents' folders are used again).
func (w *workdirs) pin(slug, dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loadLocked()
	if dir == "" {
		delete(w.by, slug)
	} else {
		w.by[slug] = workdir{Dir: dir, Pinned: true}
	}
	w.saveLocked()
}

func (w *workdirs) get(slug string) workdir {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loadLocked()
	return w.by[slug]
}

// Workdir returns the working folder the web UI uses for a project and whether a person pinned it.
func (s *Service) Workdir(slug string) (string, bool) {
	w := s.dirs.get(slug)
	return w.Dir, w.Pinned
}

// SetWorkdir pins a project's working folder (a git repository or Lore workspace on this machine); "" unpins.
func (s *Service) SetWorkdir(slug, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return errorf("%s is not a folder on this machine", dir)
		}
		if workRoot(abs) == "" {
			return errorf("%s is not in a git repository or Lore workspace", dir)
		}
		dir = abs
	}
	s.dirs.pin(slug, dir)
	return nil
}

// rememberWorkdir records the caller's folder for p when the folder's remotes are linked to p.
func (s *Service) rememberWorkdir(c Caller, p *board.Project) {
	if dir := s.linkedCwd(c, p.Slug()); dir != "" {
		s.dirs.set(p.Slug(), dir)
	}
}

// linkedCwd is the caller's folder when its remotes are linked to the project slug, else "". Knowledge pages
// are checked only against a working copy of their own project: another repository says nothing about them.
func (s *Service) linkedCwd(c Caller, slug string) string {
	if c.Cwd == "" {
		return ""
	}
	for _, k := range s.vcs.Keys(c.Cwd) {
		if got, err := s.ix.ProjectForRemote(k); err == nil && got == slug {
			return c.Cwd
		}
	}
	return ""
}

// workRoot is the top of the working copy (git or Lore, the nearer one) that holds dir, or "".
func workRoot(dir string) string {
	g, l := vcs.GitRoot(dir), vcs.LoreRoot(dir)
	if len(l) > len(g) {
		return l
	}
	return g
}

// backticked finds `code spans` in a task body.
var backticked = regexp.MustCompile("`([^`\\s]+)`")

// taskFiles maps a task to files in the working copy holding dir (spec §5): the files its asset maps to by the
// suite's resolver, then file paths written in backticks in the body that exist there. Paths are relative to
// the returned root.
func (s *Service) taskFiles(dir string, p *board.Project, asset, body string) (root string, files []string) {
	root = workRoot(dir)
	if root == "" {
		return "", nil
	}
	st, _ := s.suiteFor(p)
	if st == nil {
		return root, nil
	}
	res := st.Resolver()
	add := func(f string) {
		if !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	if asset != "" {
		for _, f := range res.Files(root, asset) {
			add(f)
		}
	}
	pathRes, _ := adapter.Resolver("path")
	for _, m := range backticked.FindAllStringSubmatch(body, -1) {
		v := m[1]
		var cand []string
		switch {
		case strings.HasPrefix(v, "/") && res.Name() != "path":
			cand = res.Files(root, v)
		case strings.Contains(v, "/") || strings.Contains(v, `\`) || filepath.Ext(v) != "":
			cand = pathRes.Files(root, v)
		}
		for _, f := range cand {
			if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil && !fi.IsDir() {
				add(f)
			}
		}
	}
	return root, files
}

// lockState asks the project's lock providers about files (relative to root), each provider in its own
// working copy, through the shared 60 s cache.
func (s *Service) lockState(ctx context.Context, p *board.Project, dir, root string, files []string) LockState {
	ls := LockState{Locks: []LockOut{}}
	if root == "" || len(files) == 0 {
		return ls
	}
	st, _ := s.suiteFor(p)
	if st == nil {
		return ls
	}
	type result struct {
		name  string
		locks []adapter.Lock
		err   error
		rel   map[string]string // provider-relative path → root-relative path
	}
	var wg sync.WaitGroup
	results := make([]*result, len(st.Adapters.Locks))
	for i, name := range st.Adapters.Locks {
		prov, err := adapter.LockProviderNamed(name)
		if err != nil {
			continue
		}
		pr := prov.Root(dir)
		if pr == "" {
			continue
		}
		r := &result{name: name, rel: map[string]string{}}
		for _, f := range files {
			if rel, err := filepath.Rel(pr, filepath.Join(root, filepath.FromSlash(f))); err == nil && !strings.HasPrefix(rel, "..") {
				r.rel[filepath.ToSlash(rel)] = f
			}
		}
		if len(r.rel) == 0 {
			continue
		}
		results[i] = r
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.locks, r.err = s.lockCache.Locks(ctx, prov, pr, slices.Collect(maps.Keys(r.rel)))
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r == nil || errors.Is(r.err, adapter.ErrNoLocks) {
			continue
		}
		ls.Checked = true
		if r.err != nil {
			ls.Unknown = append(ls.Unknown, r.err.Error())
			continue
		}
		for _, l := range adapter.Match(r.locks, slices.Sorted(maps.Keys(r.rel))) {
			ls.Locks = append(ls.Locks, LockOut{Path: r.rel[l.Path], Owner: l.Owner, Since: l.Since, Provider: r.name})
		}
	}
	return ls
}

// lockLines renders lock state for a task_show reply.
func lockLines(ls LockState) []string {
	var out []string
	for _, l := range ls.Locks {
		s := fmt.Sprintf("locked: %s by %s", l.Path, orUnknown(l.Owner))
		if l.Since != "" {
			s += " since " + l.Since
		}
		out = append(out, s+" ("+l.Provider+")")
	}
	for _, u := range ls.Unknown {
		out = append(out, "lock state unknown: "+u)
	}
	if ls.Checked && len(ls.Locks) == 0 && len(ls.Unknown) == 0 {
		out = append(out, "locks: none of these files is locked")
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "someone (owner not reported)"
	}
	return s
}

// TaskLocks returns a task's files and their lock state for the web UI, in the working folder an agent last
// used the task's project from. dir is "" when no agent has worked in the project since the server started.
func (s *Service) TaskLocks(ctx context.Context, id string) (dir string, files []string, ls LockState, err error) {
	p, id, err := s.projectOfID(id)
	if err != nil {
		return "", nil, LockState{}, err
	}
	t, err := p.Get(id)
	if err != nil {
		return "", nil, LockState{}, err
	}
	dir = s.dirs.get(p.Slug()).Dir
	root, files := s.taskFiles(dir, p, t.Asset, t.Body)
	return dir, files, s.lockState(ctx, p, dir, root, files), nil
}
