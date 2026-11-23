package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// ErrNoLocks is returned by a lock provider when the repository does not use its locks (git LFS is not
// installed or no file is lockable). It is not shown as "lock state unknown".
var ErrNoLocks = errors.New("this repository does not use these locks")

// LockTimeout bounds every lock command (spec §5: 3 s, never a prompt or a hang).
var LockTimeout = 3 * time.Second

// LockTTL is how long a repository's lock list (or the failure to read it) is reused.
var LockTTL = 60 * time.Second

// runCmd runs a tool in dir with env and returns stdout, stderr and the error. Tests replace it.
var runCmd = func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, env, nil
	// A helper the tool started (git-lfs, a credential manager) can keep the pipes open after the tool is
	// killed; WaitDelay stops waiting for them, so a timeout really ends the call.
	cmd.WaitDelay = 300 * time.Millisecond
	vcs.HideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// cmdError makes a short, readable error from a failed command: its first message line, or the timeout.
func cmdError(ctx context.Context, name string, stdout, stderr []byte, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s did not answer within %s", name, LockTimeout)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is not installed", name)
	}
	for _, b := range [][]byte{stderr, stdout} {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				l = strings.TrimSpace(strings.TrimPrefix(l, "[Error]"))
				if len(l) > 160 {
					l = l[:160] + "…"
				}
				return fmt.Errorf("%s: %s", name, l)
			}
		}
	}
	return fmt.Errorf("%s: %v", name, err)
}

// ---- git-lfs ----

type gitLFS struct{}

func (gitLFS) Name() string { return "git-lfs" }

func (gitLFS) Root(dir string) string { return vcs.GitRoot(dir) }

// lfsInstalled caches whether `git lfs` runs, for FoundTTL-like periods.
var lfsInstalled struct {
	sync.Mutex
	ok      bool
	expires time.Time
}

func hasGitLFS(ctx context.Context) bool {
	lfsInstalled.Lock()
	defer lfsInstalled.Unlock()
	if time.Now().Before(lfsInstalled.expires) {
		return lfsInstalled.ok
	}
	_, _, err := runCmd(ctx, "", vcs.GitEnv(), "git", "lfs", "version")
	lfsInstalled.ok, lfsInstalled.expires = err == nil, time.Now().Add(5*time.Minute)
	return lfsInstalled.ok
}

// lockable reports whether any of files is lockable in the repository at root. The top .gitattributes is
// read first (the usual place, no process); otherwise `git check-attr` answers for nested .gitattributes too.
func lockable(ctx context.Context, root string, files []string) bool {
	if hasLockable(root) {
		return true
	}
	if len(files) == 0 {
		return false
	}
	args := append([]string{"-C", root, "check-attr", "lockable", "--"}, files...)
	out, _, err := runCmd(ctx, root, vcs.GitEnv(), "git", args...)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), ": lockable: set") {
			return true
		}
	}
	return false
}

// hasLockable reports whether the repository's top .gitattributes marks any file lockable.
func hasLockable(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		for _, f := range strings.Fields(l)[1:] {
			if f == "lockable" {
				return true
			}
		}
	}
	return false
}

// lfsEnv is the git environment for asking the LFS server, with every way of asking a person turned off: no
// terminal prompt, no credential manager dialog, no ssh passphrase prompt. It keeps the system git config,
// where Git for Windows sets the credential helper, so saved credentials are used (QM-45).
func lfsEnv() []string {
	return append(vcs.GitRemoteEnv(), "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
}

func (g gitLFS) Locks(ctx context.Context, root string, files []string) ([]Lock, error) {
	if root == "" || !lockable(ctx, root, files) || !hasGitLFS(ctx) {
		return nil, ErrNoLocks
	}
	out, errb, err := runCmd(ctx, root, lfsEnv(), "git", "-c", "core.fsmonitor=", "-c", "credential.interactive=never",
		"-C", root, "lfs", "locks", "--json")
	if err != nil {
		return nil, cmdError(ctx, "git lfs locks", out, errb, err)
	}
	locks, err := parseLFSLocks(out)
	// git-lfs 3.5 prints [] and exits 0 when the server wants a login it cannot get ("unable to get password
	// from user"); the failure is only on stderr. An empty list with an error message is an unknown state.
	if err == nil && len(locks) == 0 && len(bytes.TrimSpace(errb)) > 0 {
		return nil, cmdError(ctx, "git lfs locks", nil, errb, errors.New("failed"))
	}
	return locks, err
}

// parseLFSLocks reads `git lfs locks --json`: an array of {id, path, owner: {name}, locked_at}.
func parseLFSLocks(out []byte) ([]Lock, error) {
	var raw []struct {
		Path  string `json:"path"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		LockedAt string `json:"locked_at"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("git lfs locks: unexpected output: %v", err)
	}
	locks := make([]Lock, 0, len(raw))
	for _, r := range raw {
		locks = append(locks, Lock{Path: cleanLockPath(r.Path), Owner: r.Owner.Name, Since: r.LockedAt})
	}
	return locks, nil
}

// ---- lore ----

type lore struct{}

func (lore) Name() string { return "lore" }

func (lore) Root(dir string) string { return vcs.LoreRoot(dir) }

func (lore) Locks(ctx context.Context, root string, files []string) ([]Lock, error) {
	if root == "" || len(files) == 0 {
		return nil, ErrNoLocks
	}
	args := append([]string{"--non-interactive", "--no-pager", "--repository", root, "lock", "status", "--"}, files...)
	out, errb, err := runCmd(ctx, root, os.Environ(), "lore", args...)
	if err != nil || bytes.Contains(out, []byte("[Error]")) || bytes.Contains(errb, []byte("[Error]")) {
		return nil, cmdError(ctx, "lore lock status", out, errb, err)
	}
	return parseLoreLocks(out), nil
}

// loreLockLine is a lock line of `lore lock status` / `lore lock query` (checked against Lore 0.14):
//
//	Content/Props/SM Odd Name.uasset by <unknown> on Fri, 20 Nov 2026 02:05:31 +0000   (status)
//	Content/Props/SM_Bridge.uasset by alice on branch e726318bbc3fd75ac8733a7e030cc35b   (query)
//
// Paths may hold spaces, so the path is everything before the last " by ". "<unknown>" is a lock taken
// without a signed-in identity.
var loreLockLine = regexp.MustCompile(`^(.+) by (.+?) on (.+)$`)

// parseLoreLocks reads the lock lines of `lore lock status`; header lines ("Files locked for edit:", "Locks
// found:") and notes ("Ignoring invalid path: …") are skipped.
func parseLoreLocks(out []byte) []Lock {
	var locks []Lock
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		m := loreLockLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		l := Lock{Path: cleanLockPath(m[1]), Owner: m[2]}
		if l.Owner == "<unknown>" {
			l.Owner = ""
		}
		if t, err := time.Parse(time.RFC1123Z, m[3]); err == nil {
			l.Since = t.UTC().Format(time.RFC3339)
		}
		locks = append(locks, l)
	}
	return locks
}

// cleanLockPath makes a provider's path repo-relative and slash-separated.
func cleanLockPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	return strings.TrimPrefix(strings.TrimPrefix(p, "./"), "/")
}

// ---- cache and matching ----

// LockCache keeps each repository's lock list (or the error reading it) for LockTTL, so opening a task again
// does not ask the lock server again.
type LockCache struct {
	mu       sync.Mutex
	entries  map[string]lockEntry
	inflight map[string]*lockCall // asks running now: a second ask for the same files waits for the first
	now      func() time.Time
}

type lockCall struct {
	done  chan struct{}
	locks []Lock
	err   error
}

type lockEntry struct {
	locks   []Lock
	err     error
	expires time.Time
}

// NewLockCache returns an empty cache.
func NewLockCache() *LockCache {
	return &LockCache{entries: map[string]lockEntry{}, inflight: map[string]*lockCall{}, now: time.Now}
}

// Locks returns the provider's locks on files in the repository at root, from the cache when fresh. The call
// is bounded by LockTimeout and does not end with ctx: a caller that goes away (a browser reload) must not
// leave a "cancelled" answer in the cache. Asks for the same files at the same time share one command.
func (c *LockCache) Locks(ctx context.Context, p LockProvider, root string, files []string) ([]Lock, error) {
	files = slices.Clone(files)
	sort.Strings(files)
	key := p.Name() + "\x00" + strings.ToLower(filepath.Clean(root)) + "\x00" + strings.Join(files, "\x00")
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.locks, e.err
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.locks, call.err
	}
	call := &lockCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), LockTimeout)
	call.locks, call.err = p.Locks(tctx, root, files)
	cancel()

	c.mu.Lock()
	delete(c.inflight, key)
	now := c.now()
	for k, e := range c.entries { // drop stale entries, so the cache does not grow for the server's lifetime
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	if !errors.Is(call.err, context.Canceled) {
		c.entries[key] = lockEntry{locks: call.locks, err: call.err, expires: now.Add(LockTTL)}
	}
	c.mu.Unlock()
	close(call.done)
	return call.locks, call.err
}

// Match returns the locks that cover one of files (repo-relative, slash-separated). Paths compare without
// case: Unreal and Windows ignore it.
func Match(locks []Lock, files []string) []Lock {
	var out []Lock
	for _, f := range files {
		for _, l := range locks {
			if strings.EqualFold(l.Path, f) {
				m := l
				m.Path = f
				out = append(out, m)
				break
			}
		}
	}
	return out
}
