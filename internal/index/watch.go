package index

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Debounce is how long the watcher waits after the last event for a file before reading it, so editors
// that write a file in several steps are read once.
var Debounce = 200 * time.Millisecond

// Change says what the watcher refreshed. Kind is "task", "activity", "project", "releases", "suites" (a file
// in suites/), "gone" (a folder was removed) or "rebuild" (everything was read again after lost events).
type Change struct {
	Kind    string
	Project string
	Path    string
}

// Watcher keeps the index fresh from file system events.
type Watcher struct {
	ix       *Index
	fsw      *fsnotify.Watcher
	onChange func(Change)
	onError  func(error)

	mu      sync.Mutex
	timers  map[string]*time.Timer
	closing bool           // set on shutdown: no new callbacks start
	running sync.WaitGroup // callbacks in progress
	done    chan struct{}
}

// Watch starts watching the board's project folders. onChange (optional) is called after each refresh
// that changed the index; onError (optional) gets refresh errors. Stop it by cancelling ctx.
func (ix *Index) Watch(ctx context.Context, onChange func(Change), onError func(error)) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{ix: ix, fsw: fsw, onChange: onChange, onError: onError, timers: map[string]*time.Timer{}, done: make(chan struct{})}
	projectsDir := filepath.Join(ix.b.Root(), "projects")
	if err := fsw.Add(projectsDir); err != nil {
		fsw.Close()
		return nil, err
	}
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		fsw.Close()
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			w.addProject(filepath.Join(projectsDir, e.Name()))
		}
	}
	// The board folder itself, only to notice suites/ appearing, and suites/ for suite file edits.
	w.report(fsw.Add(ix.b.Root()))
	if err := fsw.Add(filepath.Join(ix.b.Root(), "suites")); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, fs.ErrNotExist) {
		w.report(err)
	}
	go w.loop(ctx)
	return w, nil
}

// Done is closed when the watcher has stopped and no refresh is still running.
func (w *Watcher) Done() <-chan struct{} { return w.done }

func (w *Watcher) addProject(dir string) {
	for _, d := range []string{dir, filepath.Join(dir, "tasks"), filepath.Join(dir, "archive"), filepath.Join(dir, "kb")} {
		if err := w.fsw.Add(d); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, fs.ErrNotExist) {
			w.report(err)
		}
	}
}

// Unwatch stops watching a project folder that is about to be moved away: Windows will not move a folder
// while its subfolders are watched. Rewatch undoes it if the move failed.
func (w *Watcher) Unwatch(dir string) {
	for _, d := range []string{filepath.Join(dir, "tasks"), filepath.Join(dir, "archive"), filepath.Join(dir, "kb"), dir} {
		_ = w.fsw.Remove(d) // not watched (yet) is fine
	}
}

// Rewatch watches a project folder again after Unwatch.
func (w *Watcher) Rewatch(dir string) { w.addProject(dir) }

func (w *Watcher) report(err error) {
	if err != nil && w.onError != nil {
		w.onError(err)
	}
}

func (w *Watcher) loop(ctx context.Context) {
	defer close(w.done)
	defer w.fsw.Close()
	projectsDir := filepath.Clean(filepath.Join(w.ix.b.Root(), "projects"))
	rootDir := filepath.Clean(w.ix.b.Root())
	suitesDir := filepath.Join(rootDir, "suites")
	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			w.closing = true
			for _, t := range w.timers {
				t.Stop()
			}
			w.mu.Unlock()
			w.running.Wait() // a refresh may be writing files (renumbering); let it finish
			return
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				// Too many changes at once (a branch switch): events were lost, so read everything again.
				w.schedule("<rebuild>", w.rebuild)
				continue
			}
			w.report(err)
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			path := filepath.Clean(ev.Name)
			name := filepath.Base(path)
			parent := filepath.Dir(path)
			switch {
			case parent == rootDir && name == "suites":
				if fi, err := os.Stat(path); err == nil && fi.IsDir() {
					w.report(w.fsw.Add(path))
				}
				w.schedule(path, w.suitesChanged)
			case parent == suitesDir:
				w.schedule(suitesDir, w.suitesChanged)
			case parent == projectsDir:
				// A new project folder, or one created in steps: watch it and pick up what is there. A folder
				// that was removed or moved away takes its rows with it.
				if fi, err := os.Stat(path); err == nil && fi.IsDir() {
					w.addProject(path)
					w.schedule(path, func() { w.scanProject(path) })
				} else if err != nil {
					w.schedule(path, func() { w.gone(path) })
				}
			case filepath.Dir(parent) == projectsDir && name == "kb":
				// The kb folder appeared (the first page, or a pull): watch it and read its pages.
				if fi, err := os.Stat(path); err == nil && fi.IsDir() {
					w.report(w.fsw.Add(path))
					w.schedule(path, func() { w.scanPages(path) })
				} else if err != nil {
					w.schedule(path, func() { w.gone(path) })
				}
			case isPageFile(path) && filepath.Dir(filepath.Dir(parent)) == projectsDir:
				w.schedule(path, func() { w.refreshPage(path) })
			case filepath.Dir(parent) == projectsDir && (name == "tasks" || name == "archive"):
				if fi, err := os.Stat(path); err == nil && fi.IsDir() {
					if err := w.fsw.Add(path); err != nil {
						w.report(err)
					}
					w.schedule(path, func() { w.scanProject(parent) })
				} else if err != nil {
					w.schedule(path, func() { w.gone(path) })
				}
			case filepath.Dir(parent) == projectsDir && name == "activity.jsonl":
				slug := filepath.Base(parent)
				w.schedule(path, func() {
					err := w.ix.RefreshActivity(slug)
					w.report(err)
					if err == nil {
						w.changed(Change{Kind: "activity", Project: slug, Path: path})
					}
				})
			case filepath.Dir(parent) == projectsDir && name == "project.toml":
				slug := filepath.Base(parent)
				w.schedule(path, func() {
					err := w.ix.RefreshProject(slug)
					w.report(err)
					if err == nil {
						w.changed(Change{Kind: "project", Project: slug, Path: path})
					}
				})
			case filepath.Dir(parent) == projectsDir && name == "releases.toml":
				slug := filepath.Base(parent)
				w.schedule(path, func() { w.report(w.ix.RefreshReleases(slug)) })
			case isTaskFileName(name) && (filepath.Base(parent) == "tasks" || filepath.Base(parent) == "archive"):
				w.schedule(path, func() { w.refreshTask(path) })
			}
		}
	}
}

func (w *Watcher) refreshTask(path string) {
	changed, err := w.ix.RefreshFile(path)
	w.report(err)
	if changed {
		w.changed(Change{Kind: "task", Project: projectSlugOf(path), Path: path})
	}
}

func (w *Watcher) refreshPage(path string) {
	changed, err := w.ix.RefreshPage(path)
	w.report(err)
	if changed {
		w.changed(Change{Kind: "kb", Project: projectSlugOf(path), Path: path})
	}
}

// scanPages refreshes every page file of a kb folder.
func (w *Watcher) scanPages(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.report(err)
		return
	}
	for _, e := range entries {
		if path := filepath.Join(dir, e.Name()); !e.IsDir() && isPageFile(path) {
			w.refreshPage(path)
		}
	}
}

// scanProject refreshes every task file of a project folder (used when a folder appears).
func (w *Watcher) scanProject(dir string) {
	p, err := w.ix.b.Project(filepath.Base(dir))
	if err != nil {
		return // no project.toml yet; its own event will come
	}
	if err := w.ix.RefreshProject(p.Slug()); err != nil {
		w.report(err)
	}
	paths, err := taskFiles(p)
	if err != nil {
		w.report(err)
		return
	}
	for _, path := range paths {
		w.refreshTask(path)
	}
	if err := w.ix.RefreshActivity(p.Slug()); err != nil {
		w.report(err)
	}
	w.scanPages(p.KBDir())
}

// suitesChanged tells listeners (the web UI) that suite files changed; suites reload themselves on next use.
func (w *Watcher) suitesChanged() {
	w.ix.Notify(Change{Kind: "suites"})
	w.changed(Change{Kind: "suites"})
}

func (w *Watcher) changed(c Change) {
	if w.onChange != nil {
		w.onChange(c)
	}
}

// gone drops the rows under a folder that was removed or moved away.
func (w *Watcher) gone(dir string) {
	if err := w.ix.RefreshGone(dir); err != nil {
		w.report(err)
		return
	}
	w.changed(Change{Kind: "gone", Project: filepath.Base(dir), Path: dir})
}

// rebuild reads the whole board again and watches every project folder (some may be new).
func (w *Watcher) rebuild() {
	if err := w.ix.Rebuild(); err != nil {
		w.report(err)
		return
	}
	projectsDir := filepath.Join(w.ix.b.Root(), "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		w.report(err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			w.addProject(filepath.Join(projectsDir, e.Name()))
		}
	}
	w.changed(Change{Kind: "rebuild"})
}

// schedule runs fn once events for key have been quiet for Debounce. Once the watcher is stopping, no new fn
// starts, and the stop waits for one that is running.
func (w *Watcher) schedule(key string, fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return
	}
	if t, ok := w.timers[key]; ok {
		t.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(Debounce, func() {
		w.mu.Lock()
		if w.timers[key] == t { // a newer timer for the same key may have replaced this one
			delete(w.timers, key)
		}
		if w.closing {
			w.mu.Unlock()
			return
		}
		w.running.Add(1)
		w.mu.Unlock()
		defer w.running.Done()
		fn()
	})
	w.timers[key] = t
}

func projectSlugOf(taskPath string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(taskPath)))
}
