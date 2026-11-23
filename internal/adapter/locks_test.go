package adapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseLFSLocks(t *testing.T) {
	locks, err := parseLFSLocks([]byte(`[{"id":"3","path":"Game/Content/SM_Bridge.uasset","owner":{"name":"alice"},"locked_at":"2026-11-20T01:02:03Z"}]`))
	if err != nil || len(locks) != 1 || locks[0].Owner != "alice" || locks[0].Path != "Game/Content/SM_Bridge.uasset" ||
		locks[0].Since != "2026-11-20T01:02:03Z" {
		t.Fatalf("locks = %+v, %v", locks, err)
	}
	if _, err := parseLFSLocks([]byte("Remote \"origin\" does not support the LFS locking API")); err == nil {
		t.Error("text output accepted")
	}
}

// Output of Lore 0.14 against a real server (captured in D:\LoreTestRepoM6).
const (
	loreStatusOut = "Files locked for edit:\n" +
		"Content/Props/SM Odd Name.uasset by <unknown> on Fri, 20 Nov 2026 02:05:31 +0000\n" +
		"Content/Props/SM_Bridge.uasset by alice on Fri, 20 Nov 2026 02:05:31 +0000\n"
	loreQueryOut = "Locks found:\n" +
		"Content/Props/SM_Bridge.uasset by alice on branch e726318bbc3fd75ac8733a7e030cc35b\n"
	loreInvalidOut = "Ignoring invalid path: Content/Nope.uasset\n"
)

func TestParseLoreLocks(t *testing.T) {
	locks := parseLoreLocks([]byte(loreStatusOut))
	if len(locks) != 2 {
		t.Fatalf("locks = %+v", locks)
	}
	if l := locks[0]; l.Path != "Content/Props/SM Odd Name.uasset" || l.Owner != "" || l.Since != "2026-11-20T02:05:31Z" {
		t.Errorf("first = %+v", l)
	}
	if l := locks[1]; l.Path != "Content/Props/SM_Bridge.uasset" || l.Owner != "alice" {
		t.Errorf("second = %+v", l)
	}
	if l := parseLoreLocks([]byte(loreQueryOut)); len(l) != 1 || l[0].Owner != "alice" || l[0].Since != "" {
		t.Errorf("query = %+v", l)
	}
	if l := parseLoreLocks([]byte(loreInvalidOut)); len(l) != 0 {
		t.Errorf("invalid path note read as a lock: %+v", l)
	}
	m := Match(locks, []string{"content/props/sm_bridge.uasset", "x.uasset"})
	if len(m) != 1 || m[0].Owner != "alice" || m[0].Path != "content/props/sm_bridge.uasset" {
		t.Errorf("Match = %+v", m)
	}
}

type countingProvider struct {
	calls int
	err   error
}

func (*countingProvider) Name() string       { return "count" }
func (*countingProvider) Root(string) string { return "r" }
func (p *countingProvider) Locks(context.Context, string, []string) ([]Lock, error) {
	p.calls++
	return []Lock{{Path: "a"}}, p.err
}

func TestLockCache(t *testing.T) {
	c := NewLockCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	p := &countingProvider{err: errors.New("offline")}
	for range 3 {
		if _, err := c.Locks(context.Background(), p, "root", []string{"a"}); err == nil {
			t.Fatal("error not kept")
		}
	}
	if p.calls != 1 {
		t.Errorf("calls = %d, want 1 (failures are cached too)", p.calls)
	}
	now = now.Add(LockTTL + time.Second)
	c.Locks(context.Background(), p, "root", []string{"a"})
	if p.calls != 2 {
		t.Errorf("calls after TTL = %d, want 2", p.calls)
	}
}

func TestHasLockable(t *testing.T) {
	dir := t.TempDir()
	if hasLockable(dir) {
		t.Error("no .gitattributes")
	}
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("# *.uasset lockable\n*.png filter=lfs\n"), 0o644)
	if hasLockable(dir) {
		t.Error("comment counted")
	}
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.uasset filter=lfs diff=lfs merge=lfs -text lockable\n"), 0o644)
	if !hasLockable(dir) {
		t.Error("lockable not seen")
	}
}

// lfsRepo makes a git repository whose lockable files are served by an LFS server at srv.
func lfsRepo(t *testing.T, srv string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if err := exec.Command("git", "lfs", "version").Run(); err != nil {
		t.Skip("git lfs is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", srv + "/rpg.git"}, {"config", "lfs.url", srv + "/rpg.git/info/lfs"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, lfsEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.uasset filter=lfs diff=lfs merge=lfs -text lockable\n"), 0o644)
	return dir
}

func TestGitLFSLocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/locks") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		w.Write([]byte(`{"locks":[{"id":"1","path":"Content/SM_Bridge.uasset","owner":{"name":"alice"},"locked_at":"2026-11-20T01:02:03Z"}]}`))
	}))
	defer srv.Close()
	dir := lfsRepo(t, srv.URL)
	locks, err := NewLockCache().Locks(context.Background(), gitLFS{}, dir, []string{"Content/SM_Bridge.uasset"})
	if err != nil {
		t.Fatal(err)
	}
	m := Match(locks, []string{"Content/SM_Bridge.uasset"})
	if len(m) != 1 || m[0].Owner != "alice" {
		t.Errorf("locks = %+v", locks)
	}
}

// An LFS server that wants a login the machine does not have, or that never answers, gives an error (lock
// state unknown) within the timeout, never a prompt or a hang.
func TestGitLFSNoLoginNoHang(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"login": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("WWW-Authenticate", `Basic realm="lfs"`)
			http.Error(w, "login required", http.StatusUnauthorized)
		},
		"silent": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(8 * time.Second):
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer func() { srv.CloseClientConnections(); srv.Close() }()
			dir := lfsRepo(t, srv.URL)
			start := time.Now()
			_, err := NewLockCache().Locks(context.Background(), gitLFS{}, dir, []string{"Content/SM_Bridge.uasset"})
			took := time.Since(start)
			if err == nil || errors.Is(err, ErrNoLocks) {
				t.Fatalf("err = %v, want lock state unknown", err)
			}
			if took > LockTimeout+2*time.Second {
				t.Errorf("took %s, want about %s at most", took, LockTimeout)
			}
			t.Logf("%s: %v (%s)", name, err, took.Round(time.Millisecond))
		})
	}
}

// A real Lore workspace without a reachable remote gives an error, not a hang.
func TestLoreOffline(t *testing.T) {
	if _, err := exec.LookPath("lore"); err != nil {
		t.Skip("lore is not installed")
	}
	dir := t.TempDir()
	cmd := exec.Command("lore", "--offline", "repository", "create", "Test")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("lore repository create: %v\n%s", err, out)
	}
	if (lore{}).Root(filepath.Join(dir, "sub")) != dir {
		t.Fatal("Root does not find the workspace")
	}
	_, err := NewLockCache().Locks(context.Background(), lore{}, dir, []string{"a.uasset"})
	if err == nil || errors.Is(err, ErrNoLocks) || !strings.Contains(err.Error(), "remote") {
		t.Errorf("err = %v, want an unknown lock state about the remote", err)
	}
}

// TestLoreServer reads real locks from a Lore workspace with a reachable server. Set QUEST_MAP_LORE_WS to the
// workspace and QUEST_MAP_LORE_LOCKED to a file locked there (repo-relative).
func TestLoreServer(t *testing.T) {
	ws, locked := os.Getenv("QUEST_MAP_LORE_WS"), os.Getenv("QUEST_MAP_LORE_LOCKED")
	if ws == "" || locked == "" {
		t.Skip("QUEST_MAP_LORE_WS and QUEST_MAP_LORE_LOCKED are not set")
	}
	start := time.Now()
	locks, err := NewLockCache().Locks(context.Background(), lore{}, ws, []string{locked, "Content/Nope.uasset"})
	if err != nil {
		t.Fatal(err)
	}
	if m := Match(locks, []string{locked}); len(m) != 1 || m[0].Since == "" {
		t.Errorf("locks = %+v", locks)
	}
	t.Logf("locks = %+v (%s)", locks, time.Since(start).Round(time.Millisecond))
}

// A caller that goes away does not end the ask or leave "cancelled" in the cache, and asks for the same files
// at the same time share one command.
type slowProvider struct {
	calls   atomic.Int32
	release chan struct{}
}

func (*slowProvider) Name() string       { return "slow" }
func (*slowProvider) Root(string) string { return "r" }
func (p *slowProvider) Locks(ctx context.Context, _ string, _ []string) ([]Lock, error) {
	p.calls.Add(1)
	select {
	case <-p.release:
		return []Lock{{Path: "a", Owner: "mika"}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestLockCacheDetachedAndShared(t *testing.T) {
	c := NewLockCache()
	p := &slowProvider{release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	results := make([][]Lock, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = c.Locks(ctx, p, "root", []string{"a"})
		}()
	}
	time.Sleep(50 * time.Millisecond)
	cancel() // the browser reloads
	time.Sleep(50 * time.Millisecond)
	close(p.release)
	wg.Wait()
	if p.calls.Load() != 1 || len(results[0]) != 1 || len(results[1]) != 1 {
		t.Fatalf("calls %d, results %v", p.calls.Load(), results)
	}
	if locks, err := c.Locks(context.Background(), p, "root", []string{"a"}); err != nil || len(locks) != 1 || p.calls.Load() != 1 {
		t.Errorf("cached = %v %v (calls %d)", locks, err, p.calls.Load())
	}
}

// A lockable pattern in a nested .gitattributes counts (git check-attr).
func TestLockableNested(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	os.MkdirAll(filepath.Join(dir, "Game", "Content"), 0o755)
	os.WriteFile(filepath.Join(dir, "Game", ".gitattributes"), []byte("*.uasset lockable\n"), 0o644)
	if !lockable(context.Background(), dir, []string{"Game/Content/SM_Door.uasset"}) {
		t.Error("nested lockable not seen")
	}
	if lockable(context.Background(), dir, []string{"Game/readme.txt"}) {
		t.Error("a file that is not lockable counted")
	}
}

// The lock query finds the credential helper Git for Windows keeps in the system config, and still never
// asks a person.
func TestLFSEnvKeepsCredentialHelper(t *testing.T) {
	got := strings.Join(lfsEnv(), " ")
	if strings.Contains(got, "GIT_CONFIG_NOSYSTEM") || !strings.Contains(got, "GCM_INTERACTIVE=never") ||
		!strings.Contains(got, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("env = %v", got)
	}
}
