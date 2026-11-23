package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// newBareFixture is a board with project RPG (default suite gamedev-unreal), linked to nothing, and a service
// with the given detector.
func newBareFixture(t *testing.T, det *vcs.Detector) *fixture {
	t.Helper()
	root := t.TempDir()
	b, err := board.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.CreateProject("rpg", "RPG", "RPG")
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(filepath.Join(root, ".cache", "index.sqlite"), b, index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	return &fixture{t: t, b: b, p: p, ix: ix, svc: New(b, ix, det)}
}

// An agent inside a Lore workspace gets its project picked without passing project (M6 done-when).
func TestLoreWorkspacePicksProject(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".lore"), 0o755)
	os.MkdirAll(filepath.Join(ws, "Content", "Props"), 0o755)
	os.WriteFile(filepath.Join(ws, ".lore", "config.toml"), []byte("remote_url = \"lore://Lore.Studio.lan:41337/RPG\"\n\n[vfs]\nvfs_type = \"None\"\n"), 0o644)
	f := newBareFixture(t, vcs.NewDetectorWith(func(string) [][2]string { return nil }))
	a := Caller{ID: "claude-code:lore01", Client: "claude-code/2.4.1", Cwd: filepath.Join(ws, "Content", "Props")}
	if _, _, err := f.svc.Claim(a, ClaimIn{}); err == nil || !strings.Contains(err.Error(), "lore:lore.studio.lan/RPG") {
		t.Fatalf("before linking: %v", err)
	}
	_, out := must[RepoOut](t)(f.svc.RepoLink(a, RepoLinkIn{Project: "RPG"}))
	if out.Remote != "lore:lore.studio.lan/RPG" {
		t.Fatalf("linked %q", out.Remote)
	}
	f.task(&board.Task{Title: "fix bridge mesh", Doer: board.Agent})
	f.rebuild()
	text, claimed := must[ClaimOut](t)(f.svc.Claim(a, ClaimIn{}))
	if claimed.Claimed == nil || claimed.Claimed.Project != "rpg" {
		t.Errorf("claim = %q", text)
	}
}

// A task naming a locked asset shows who holds the lock, in task_show and in the web UI's lock call
// (M6 done-when), here with a git LFS server.
func TestShowLockedAssetLFS(t *testing.T) {
	if err := exec.Command("git", "lfs", "version").Run(); err != nil {
		t.Skip("git lfs is not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/locks") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		w.Write([]byte(`{"locks":[{"id":"1","path":"Content/Props/SM_Bridge.uasset","owner":{"name":"alice"},"locked_at":"2026-11-20T01:02:03Z"}]}`))
	}))
	defer srv.Close()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", srv.URL + "/acme/rpg.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.uasset filter=lfs diff=lfs merge=lfs -text lockable\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "RPG.uproject"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(repo, "Content", "Props"), 0o755)
	os.MkdirAll(filepath.Join(repo, "Config"), 0o755)
	os.WriteFile(filepath.Join(repo, "Content", "Props", "SM_Bridge.uasset"), nil, 0o644)
	os.WriteFile(filepath.Join(repo, "Config", "DefaultGame.ini"), nil, 0o644)

	f := newBareFixture(t, vcs.NewDetector())
	id := f.task(&board.Task{Title: "bridge collision", Doer: board.Agent, Asset: "/Game/Props/SM_Bridge",
		Body: "Check `Config/DefaultGame.ini` and `Missing/File.txt`."})
	f.rebuild()
	a := Caller{ID: "claude-code:lfs001", Client: "claude-code/2.4.1", Cwd: repo}
	must[RepoOut](t)(f.svc.RepoLink(a, RepoLinkIn{Project: "RPG"}))
	text, d := must[TaskDetail](t)(f.svc.Show(a, ShowIn{ID: id}))
	if strings.Join(d.Files, ",") != "Content/Props/SM_Bridge.uasset,Config/DefaultGame.ini" {
		t.Errorf("files = %v", d.Files)
	}
	if d.Locks == nil || len(d.Locks.Locks) != 1 || d.Locks.Locks[0].Owner != "alice" ||
		!strings.Contains(text, "locked: Content/Props/SM_Bridge.uasset by alice since 2026-11-20T01:02:03Z (git-lfs)") {
		t.Fatalf("show = %q\nlocks = %+v", text, d.Locks)
	}
	// The web UI asks in the folder the agent used.
	dir, files, ls, err := f.svc.TaskLocks(context.Background(), id)
	if err != nil || dir != repo || len(files) != 2 || len(ls.Locks) != 1 || ls.Locks[0].Owner != "alice" {
		t.Errorf("TaskLocks = %q %v %+v %v", dir, files, ls, err)
	}
}

func TestLockLines(t *testing.T) {
	got := lockLines(LockState{Checked: true, Unknown: []string{"git lfs locks: fatal: unable to get password from user"}})
	if len(got) != 1 || got[0] != "lock state unknown: git lfs locks: fatal: unable to get password from user" {
		t.Errorf("unknown = %q", got)
	}
	if got := lockLines(LockState{Checked: true}); len(got) != 1 || !strings.Contains(got[0], "none") {
		t.Errorf("none = %q", got)
	}
	if got := lockLines(LockState{}); len(got) != 0 {
		t.Errorf("unchecked = %q", got)
	}
}

// TestLoreServerEndToEnd runs the M6 done-when against a real Lore workspace with a reachable server: set
// QUEST_MAP_LORE_WS to a workspace that has an Unreal project with /Game/Props/SM_Bridge locked.
func TestLoreServerEndToEnd(t *testing.T) {
	ws := os.Getenv("QUEST_MAP_LORE_WS")
	if ws == "" {
		t.Skip("QUEST_MAP_LORE_WS is not set")
	}
	f := newBareFixture(t, vcs.NewDetector())
	a := Caller{ID: "claude-code:lore02", Client: "claude-code/2.4.1", Cwd: filepath.Join(ws, "Content")}
	_, link := must[RepoOut](t)(f.svc.RepoLink(a, RepoLinkIn{Project: "RPG"}))
	id := f.task(&board.Task{Title: "bridge mesh", Doer: board.Agent, Asset: "/Game/Props/SM_Bridge"})
	f.rebuild()
	_, claimed := must[ClaimOut](t)(f.svc.Claim(a, ClaimIn{})) // project picked from the workspace
	if claimed.Claimed == nil || claimed.Claimed.ID != id {
		t.Fatalf("claim = %+v", claimed)
	}
	text, d := must[TaskDetail](t)(f.svc.Show(a, ShowIn{ID: id}))
	if d.Locks == nil || len(d.Locks.Locks) != 1 || d.Locks.Locks[0].Since == "" || d.Locks.Locks[0].Provider != "lore" {
		t.Fatalf("show = %q\nlocks = %+v", text, d.Locks)
	}
	t.Logf("linked %s\n%s", link.Remote, text)
}
