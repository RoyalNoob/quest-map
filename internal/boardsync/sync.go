// Package boardsync commits, pulls and pushes a board folder kept in git (spec §3.5): the Sync button and
// auto-sync of the web UI.
package boardsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// States a sync can end in.
const (
	StateOff      = "off"      // the board is not in a git repository with an upstream branch
	StateOK       = "ok"       // committed, merged and pushed
	StateConflict = "conflict" // a merge left conflicts; auto-sync waits until the person resolves them
	StateError    = "error"    // git failed (push rejected, a file in the way, …); the next round tries again
	StateOffline  = "offline"  // the remote could not be reached; local commits stay
)

// Settings are this machine's sync choices, kept in .run/sync.json: each copy of a board decides for itself.
type Settings struct {
	Auto     bool `json:"auto"`
	Interval int  `json:"interval_minutes"` // pull and push this often while auto is on
}

// DefaultInterval is the auto-sync interval when none is set, in minutes.
const DefaultInterval = 10

// Status is what the last sync did and where the board stands.
type Status struct {
	State    string    `json:"state"`
	Message  string    `json:"message,omitempty"`
	Files    []string  `json:"files"`  // conflicted files, relative to the board root
	Ahead    int       `json:"ahead"`  // local commits not pushed
	Behind   int       `json:"behind"` // remote commits not merged
	Upstream string    `json:"upstream,omitempty"`
	LastSync time.Time `json:"last_sync"` // last sync that ended ok
	Running  bool      `json:"running"`
	Settings
}

// Syncer syncs one board. Its methods are safe for concurrent use; one sync runs at a time.
type Syncer struct {
	root string
	logf func(string, ...any)

	// OnSync, if set, is called after each sync (the server tells open UIs).
	OnSync func(Status)

	run sync.Mutex // one sync at a time
	mu  sync.Mutex // guards st
	st  Status
}

// New returns a Syncer for the board at root and reads its settings.
func New(root string, logf func(string, ...any)) *Syncer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Syncer{root: root, logf: logf, st: Status{State: StateOff, Files: []string{}}}
	s.st.Settings = s.readSettings()
	s.set(s.look(context.Background(), s.st))
	return s
}

// Timeout bounds one git command; a fetch or push over a slow network is the long one.
var Timeout = 60 * time.Second

func (s *Syncer) settingsPath() string { return filepath.Join(s.root, ".run", "sync.json") }

func (s *Syncer) readSettings() Settings {
	set := Settings{Interval: DefaultInterval}
	if data, err := os.ReadFile(s.settingsPath()); err == nil {
		json.Unmarshal(data, &set)
	}
	if set.Interval <= 0 {
		set.Interval = DefaultInterval
	}
	return set
}

// SetSettings stores this machine's auto-sync choice.
func (s *Syncer) SetSettings(set Settings) error {
	if set.Interval <= 0 {
		set.Interval = DefaultInterval
	}
	if set.Interval > 24*60 {
		return errors.New("interval: at most 1440 minutes (a day)")
	}
	data, _ := json.Marshal(set)
	if err := os.MkdirAll(filepath.Dir(s.settingsPath()), 0o700); err != nil {
		return err
	}
	if err := board.WriteFileAtomic(s.settingsPath(), data); err != nil {
		return err
	}
	s.mu.Lock()
	s.st.Settings = set
	s.mu.Unlock()
	return nil
}

// Status returns the last known status. Refresh asks git first.
func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	st.Files = append([]string{}, st.Files...)
	return st
}

// Refresh updates the status from git without syncing: whether the board has an upstream, ahead/behind
// (against the last fetch), and an unresolved merge.
func (s *Syncer) Refresh(ctx context.Context) Status {
	if !s.run.TryLock() {
		return s.Status() // a sync is running and will set it
	}
	defer s.run.Unlock()
	s.set(s.look(ctx, s.Status()))
	return s.Status()
}

func (s *Syncer) set(st Status) {
	s.mu.Lock()
	st.Settings = s.st.Settings
	if st.Files == nil {
		st.Files = []string{}
	}
	s.st = st
	s.mu.Unlock()
}

// look fills what git says now into st: off, conflict, or the ahead/behind counts.
func (s *Syncer) look(ctx context.Context, st Status) Status {
	st.Running, st.Files = false, nil
	up, err := s.git(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		st.State, st.Upstream, st.Ahead, st.Behind = StateOff, "", 0, 0
		if _, e := s.git(ctx, "rev-parse", "--is-inside-work-tree"); e != nil {
			st.Message = "the board folder is not in a git repository"
		} else {
			st.Message = "the board's git branch has no upstream; push it once with git push -u"
		}
		return st
	}
	st.Upstream = up
	// A board in a subfolder of a bigger repository would commit what the person staged elsewhere in it and
	// merge and push all of it: sync only a board that is its repository's top folder.
	if top, err := s.git(ctx, "rev-parse", "--show-toplevel"); err == nil && !sameDir(top, s.root) {
		st.State, st.Upstream, st.Ahead, st.Behind = StateOff, "", 0, 0
		st.Message = "the board folder is inside a larger git repository (" + filepath.FromSlash(top) +
			"); sync works only when the board folder is the repository's top folder"
		return st
	}
	if files, merging := s.unmerged(ctx); merging && len(files) > 0 {
		st.State, st.Files = StateConflict, files
		st.Message = "a merge left conflicts in " + strings.Join(files, ", ") + "; resolve them (keep both sides' log lines) and sync again"
	} else if st.State == StateOff || st.State == StateConflict {
		st.State, st.Message = StateOK, ""
	}
	if out, err := s.git(ctx, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
		if f := strings.Fields(out); len(f) == 2 {
			st.Ahead, _ = strconv.Atoi(f[0])
			st.Behind, _ = strconv.Atoi(f[1])
		}
	}
	return st
}

// unmerged reports whether a merge is in progress and which files still hold conflict markers. Files the
// person fixed but did not `git add` count as resolved: Sync adds them.
func (s *Syncer) unmerged(ctx context.Context) (files []string, merging bool) {
	if _, err := s.git(ctx, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err != nil {
		return nil, false
	}
	out, _ := s.git(ctx, "diff", "--name-only", "--diff-filter=U", "--relative")
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(f)))
		// A binary file (a plugin image) never has markers: it stays unresolved until someone picks a side and
		// runs git add, so a sync never keeps one side silently.
		if err == nil && !conflictMarker.Match(data) && !binary(data) {
			continue
		}
		files = append(files, f)
	}
	return files, true
}

// binary reports whether data looks like a binary file, as git decides: a NUL byte in the first 8000 bytes.
func binary(data []byte) bool { return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 }

// sameDir reports whether git's path (forward slashes) names the folder dir, following links and, on Windows,
// ignoring case.
func sameDir(gitPath, dir string) bool {
	clean := func(p string) string {
		p = filepath.Clean(filepath.FromSlash(p))
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return p
	}
	a, b := clean(gitPath), clean(dir)
	return a == b || (os.PathSeparator == '\\' && strings.EqualFold(a, b))
}

var credentialsMissing = regexp.MustCompile(`(?i)terminal prompts disabled|could not read (username|password)`)

var conflictMarker = regexp.MustCompile(`(?m)^(<<<<<<<|>>>>>>>)( |$)`)

// Sync commits the board's changes, merges the upstream branch and pushes. A merge that conflicts is left
// as git left it (files with markers are flagged, never overwritten, §3.5) until the person resolves it.
func (s *Syncer) Sync(ctx context.Context) Status {
	s.run.Lock()
	defer s.run.Unlock()
	st := s.Status()
	st.Running = true
	s.set(st)
	st = s.sync(ctx, s.look(ctx, st))
	st.Running = false
	if st.State == StateOK {
		st.LastSync = time.Now().UTC()
	}
	s.set(st)
	if st.State != StateOK {
		s.logf("sync: %s: %s", st.State, st.Message)
	}
	st = s.Status() // as stored: lists are never null
	if s.OnSync != nil {
		s.OnSync(st)
	}
	return st
}

func (s *Syncer) sync(ctx context.Context, st Status) Status {
	if st.State == StateOff || st.State == StateConflict {
		return st
	}
	fail := func(state string, err error) Status {
		st.State, st.Message = state, err.Error()
		return s.withCounts(ctx, st)
	}
	// Local steps run with board writes held back, so git never commits or merges over a write.
	if err := s.gated(func() error { return s.commit(ctx) }); err != nil {
		return fail(StateError, err)
	}
	if _, err := s.git(ctx, "fetch", "--quiet"); err != nil {
		return fail(netState(err), err)
	}
	st = s.withCounts(ctx, st)
	if st.Behind > 0 {
		err := s.gated(func() error {
			_, err := s.git(ctx, "merge", "--no-edit", "@{u}")
			return err
		})
		if err != nil {
			if files, merging := s.unmerged(ctx); merging {
				st.State, st.Files = StateConflict, files
				st.Message = "the merge left conflicts in " + strings.Join(files, ", ") + "; resolve them (keep both sides' log lines) and sync again"
				return s.withCounts(ctx, st)
			}
			return fail(StateError, err)
		}
		st = s.withCounts(ctx, st)
	}
	if st.Ahead > 0 {
		if _, err := s.git(ctx, "push", "--quiet"); err != nil {
			return fail(netState(err), err)
		}
		st = s.withCounts(ctx, st)
	}
	st.State, st.Message, st.Files = StateOK, "", nil
	return st
}

// commit stages everything under the board root and commits it, finishing a merge the person resolved.
func (s *Syncer) commit(ctx context.Context) error {
	if _, err := s.git(ctx, "add", "-A", "--", "."); err != nil {
		return err
	}
	_, merging := s.unmerged(ctx)
	if _, err := s.git(ctx, "diff", "--cached", "--quiet"); err == nil && !merging {
		return nil // nothing to commit
	}
	if merging {
		_, err := s.git(ctx, "commit", "--no-edit", "--quiet")
		return err
	}
	host, _ := os.Hostname()
	_, err := s.git(ctx, "commit", "--quiet", "-m", fmt.Sprintf("quest-map sync: %s %s", host, time.Now().UTC().Format("2006-01-02 15:04Z")))
	return err
}

func (s *Syncer) withCounts(ctx context.Context, st Status) Status {
	if out, err := s.git(ctx, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
		if f := strings.Fields(out); len(f) == 2 {
			st.Ahead, _ = strconv.Atoi(f[0])
			st.Behind, _ = strconv.Atoi(f[1])
		}
	}
	return st
}

func (s *Syncer) gated(op func() error) error {
	board.WriteGate.Lock()
	defer board.WriteGate.Unlock()
	return op()
}

// netState tells an unreachable remote from other failures (a rejected push, an auth error).
func netState(err error) string {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"could not resolve host", "unable to access", "could not read from remote", "connection timed out", "network is unreachable", "connection refused"} {
		if strings.Contains(msg, s) {
			return StateOffline
		}
	}
	return StateError
}

// git runs one git command in the board root and returns its trimmed output; an error carries git's message.
func (s *Syncer) git(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	// No credential questions: the helper answers from what it saved, or the call fails (GitRemoteEnv).
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", s.root, "-c", "credential.interactive=never"}, args...)...)
	cmd.Env = vcs.GitRemoteEnv()
	vcs.HideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		if credentialsMissing.MatchString(msg) {
			msg += " – git has no saved credentials for this remote: push once from a terminal or your git client, so its credential helper stores them"
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Loop runs auto-sync until ctx ends: on start, every interval, and a minute after quest-map last wrote the
// board, so local changes leave soon. While a merge waits for the person, each round only looks.
func (s *Syncer) Loop(ctx context.Context) {
	tick := time.NewTicker(LoopTick)
	defer tick.Stop()
	var tried time.Time // last auto-sync, ok or not: a failing remote is tried again after the interval
	for {
		st := s.Status()
		if st.Auto && st.State != StateOff {
			lw := board.LastWrite()
			if time.Since(tried) >= time.Duration(st.Interval)*time.Minute || (lw.After(tried) && time.Since(lw) >= WriteDelay) {
				tried = time.Now()
				s.Sync(ctx)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// LoopTick and WriteDelay pace auto-sync: how often it checks whether a sync is due, and how long after the
// last write it waits (more writes may follow).
var (
	LoopTick   = 15 * time.Second
	WriteDelay = time.Minute
)
