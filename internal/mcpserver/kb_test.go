package mcpserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/kb"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// gitRepo makes a git working copy of the fixture's RPG repository (its origin is linked to RPG, read by the real
// detector) with committed files, and returns its folder and a commit function.
func gitRepo(t *testing.T, f *fixture) (string, func(file, content, msg string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "https://github.com/acme/rpg.git") // linked to the fixture's rpg project
	commit := func(file, content, msg string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(file))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-q", "-m", msg)
	}
	commit("Source/Collision/BridgeGen.cpp", "// bridge collision v1\n", "first")
	commit("Source/Other.cpp", "// other\n", "second")
	f.svc.vcs = vcs.NewDetector()
	f.rebuild() // the index learns the RPG repository link
	return dir, commit
}

// TestKnowledgeBaseDoneWhen is the M9 acceptance check (§11): an agent writes a page with path sources; after a
// commit that changes one of them, kb_find from that working copy shows the page as stale and names the file,
// and kb_write recheck makes it fresh; a second agent session finds the page by a question in other words.
func TestKnowledgeBaseDoneWhen(t *testing.T) {
	f := newFixture(t)
	repo, commit := gitRepo(t, f)
	a := Caller{ID: "claude-code:aaaa", Client: "claude-code/2.4.1", Cwd: filepath.Join(repo, "Source")}
	task := f.task(&board.Task{Title: "Player falls through the bridge", Doer: board.Agent})

	text, out := must[PageOut](t)(f.svc.KBWrite(a, KBWriteIn{Project: "rpg",
		Title:    "How bridge collision is generated",
		Question: "Why does the player fall through SM_Bridge_Mid?",
		Body:     "Collision for bridge meshes comes from BridgeGen.cpp, which builds convex hulls per segment.",
		Tags:     []string{"physics", "level"},
		Sources:  []string{filepath.Join(repo, "Source", "Collision", "BridgeGen.cpp"), "task:" + task}}))
	if out.Ref != "rpg/how-bridge-collision-is-generated" || out.Freshness.Status != kb.Fresh {
		t.Fatalf("write = %q %+v", text, out)
	}
	if out.Sources[0] != "Source/Collision/BridgeGen.cpp" {
		t.Errorf("absolute source not made repo-relative: %v", out.Sources)
	}

	// The same title again is refused with the existing page named.
	if _, _, err := f.svc.KBWrite(a, KBWriteIn{Project: "rpg", Title: "How bridge collision is generated", Body: "x"}); err == nil ||
		!strings.Contains(err.Error(), "rpg/how-bridge-collision-is-generated") {
		t.Errorf("duplicate title: %v", err)
	}

	// A second session asks in other words and finds it before reading any code.
	b := Caller{ID: "codex:bbbb", Client: "codex/1.0", Cwd: repo}
	text, found := must[KBFindOut](t)(f.svc.KBFind(b, KBFindIn{Project: "rpg", Query: "player keeps falling through the bridge mesh"}))
	if len(found.Pages) == 0 || found.Pages[0].Ref != out.Ref || found.Pages[0].Freshness.Status != kb.Fresh {
		t.Fatalf("other words: %q %+v", text, found.Pages)
	}

	// A commit that changes a source makes the page stale, naming the file; an unrelated commit does not.
	commit("Source/Other.cpp", "// other v2\n", "unrelated")
	_, found = must[KBFindOut](t)(f.svc.KBFind(b, KBFindIn{Project: "rpg", Query: "bridge collision"}))
	if found.Pages[0].Freshness.Status != kb.Fresh {
		t.Errorf("after an unrelated commit: %+v", found.Pages[0].Freshness)
	}
	commit("Source/Collision/BridgeGen.cpp", "// bridge collision v2\n", "change the source")
	text, found = must[KBFindOut](t)(f.svc.KBFind(b, KBFindIn{Project: "rpg", Query: "bridge collision"}))
	fr := found.Pages[0].Freshness
	if fr.Status != kb.Stale || len(fr.Changed) != 1 || fr.Changed[0] != "Source/Collision/BridgeGen.cpp" || !strings.Contains(text, "STALE") {
		t.Fatalf("after changing the source: %q %+v", text, fr)
	}
	if _, found := must[KBFindOut](t)(f.svc.KBFind(b, KBFindIn{Project: "rpg", Query: "bridge stale:yes"})); len(found.Pages) != 1 {
		t.Errorf("stale:yes = %+v", found.Pages)
	}

	// kb_write recheck makes it fresh again.
	text, out = must[PageOut](t)(f.svc.KBWrite(b, KBWriteIn{Page: out.Ref, Recheck: true}))
	if out.Freshness.Status != kb.Fresh || out.Checked.By != "codex:bbbb" {
		t.Fatalf("recheck: %q %+v", text, out)
	}

	// A task source goes stale when its task changes status after the check; task_show lists the page.
	review := board.Review
	f.b.Now = func() time.Time { return time.Now().Add(5 * time.Second) } // the change comes after the check
	if _, err := f.p.Update(task, me, board.Edit{Status: &review}, "", nil); err != nil {
		t.Fatal(err)
	}
	f.ix.RefreshActivity("rpg")
	text, page := must[PageOut](t)(f.svc.KBShow(b, KBShowIn{Page: out.Ref}))
	if page.Freshness.Status != kb.Stale || !strings.Contains(strings.Join(page.Freshness.Changed, ","), "task:"+task) || !strings.Contains(text, "convex hulls") {
		t.Errorf("task source: %q %+v", text, page.Freshness)
	}
	text, _ = must[TaskDetail](t)(f.svc.Show(b, ShowIn{ID: task}))
	if !strings.Contains(text, out.Ref) {
		t.Errorf("task_show does not list the page:\n%s", text)
	}

	// Retired pages leave searches; the web UI (no working copy) sees "unknown".
	retired := true
	must[PageOut](t)(f.svc.KBWrite(b, KBWriteIn{Page: out.Ref, Retired: &retired}))
	if _, found := must[KBFindOut](t)(f.svc.KBFind(b, KBFindIn{Project: "rpg", Query: "bridge"})); len(found.Pages) != 0 {
		t.Errorf("retired page still found: %+v", found.Pages)
	}
	row, _ := f.svc.pageRow(f.p, "how-bridge-collision-is-generated")
	if fr := kb.Check(f.ix, row, ""); fr.Status != kb.Unknown && fr.Status != kb.Stale {
		t.Errorf("no working copy: %+v", fr)
	}

	// Every write left a kb activity record.
	recs, _, _ := f.p.Activity()
	n := 0
	for _, r := range recs {
		if r.Kind == board.KindKB && r.Page == "how-bridge-collision-is-generated" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("kb records = %d, want 3 (create, recheck, retire)", n)
	}
}

func TestPageKeepsUnknownKeysAndRefusesBrokenFiles(t *testing.T) {
	f := newFixture(t)
	dir := f.p.KBDir()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "hand-written.md"), []byte("---\ntitle: Hand written\nowner: lead   # kept\ntags: [a]\n---\n\nBody.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.md"), []byte("---\ntitle: [\n---\n"), 0o644)
	f.rebuild()
	a := agentCaller("cccc")
	must[PageOut](t)(f.svc.KBWrite(a, KBWriteIn{Page: "rpg/hand-written", Body: "New body."}))
	data, _ := os.ReadFile(filepath.Join(dir, "hand-written.md"))
	if !strings.Contains(string(data), "owner: lead") || !strings.Contains(string(data), "New body.") {
		t.Errorf("unknown key or body lost:\n%s", data)
	}
	if _, _, err := f.svc.KBWrite(a, KBWriteIn{Page: "rpg/broken", Body: "x"}); err == nil {
		t.Error("a page that does not parse was overwritten")
	}
	if probs, _ := f.ix.ParseProblems(); len(probs) != 1 || !strings.HasSuffix(filepath.ToSlash(probs[0].Path), "kb/broken.md") {
		t.Errorf("parse problems = %+v", probs)
	}
}

// A page's checked.rev comes from a file anyone with the board can edit (a hand edit, a merged board copy): an
// option there must never reach git, where --output=<file> would overwrite that file.
func TestCheckedRevIsNotAGitOption(t *testing.T) {
	f := newFixture(t)
	repo, _ := gitRepo(t, f)
	target := filepath.Join(t.TempDir(), "pwned.txt")
	dir := f.p.KBDir()
	os.MkdirAll(dir, 0o755)
	page := "---\ntitle: Evil\nsources:\n  - path: Source/Other.cpp\nchecked: {rev: \"--output=" + filepath.ToSlash(target) +
		"\", at: 2026-11-20T08:00:00Z, by: x}\n---\n\nBody.\n"
	os.WriteFile(filepath.Join(dir, "evil.md"), []byte(page), 0o644)
	f.rebuild()
	_, out := must[PageOut](t)(f.svc.KBShow(Caller{ID: "a", Cwd: repo}, KBShowIn{Project: "rpg", Page: "evil"}))
	if out.Freshness.Status != kb.Unknown || !strings.Contains(out.Freshness.Reason, "commit id") {
		t.Errorf("freshness = %+v", out.Freshness)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("git wrote the file named in checked.rev")
	}
}

// The stale: filter and the stale-last order apply before the limit, so a stale page below the cut is found.
func TestStaleFilterBeforeLimit(t *testing.T) {
	f := newFixture(t)
	repo, commit := gitRepo(t, f)
	a := Caller{ID: "claude-code:aaaa", Cwd: repo}
	must[PageOut](t)(f.svc.KBWrite(a, KBWriteIn{Project: "rpg", Title: "A fresh page", Body: "x", Sources: []string{"Source/Other.cpp"}}))
	must[PageOut](t)(f.svc.KBWrite(a, KBWriteIn{Project: "rpg", Title: "B stale page", Body: "x", Sources: []string{"Source/Collision"}}))
	commit("Source/Collision/BridgeGen.cpp", "// v2\n", "change")
	q, _ := kb.ParseQuery("stale:yes")
	q.Project, q.Limit = "rpg", 1
	res, err := kb.Find(f.ix, q, func(string) string { return repo })
	if err != nil || len(res) != 1 || res[0].Slug != "b-stale-page" || res[0].Freshness.Changed[0] != "Source/Collision/BridgeGen.cpp" {
		t.Fatalf("stale:yes limit 1 = %+v, %v", res, err)
	}
}

// A working copy of another project's repository says nothing about a page (QM-33): writing from it records no
// revision, the page stays unknown, and the reply says why.
func TestPageNotCheckedAgainstUnlinkedRepo(t *testing.T) {
	f := newFixture(t)
	repo, _ := gitRepo(t, f) // linked to RPG, not to TOOLS
	if _, err := f.b.CreateProject("tools", "TOOLS", "Tools"); err != nil {
		t.Fatal(err)
	}
	f.rebuild()
	a := Caller{ID: "claude-code:aaaa", Cwd: repo}
	text, out := must[PageOut](t)(f.svc.KBWrite(a, KBWriteIn{Project: "tools", Title: "Jump", Body: "x", Sources: []string{"Source/Other.cpp"}}))
	if out.Freshness.Status != kb.Unknown || !strings.Contains(text, "not a repository linked to TOOLS") {
		t.Errorf("write = %q %+v", text, out.Freshness)
	}
	_, found := must[KBFindOut](t)(f.svc.KBFind(a, KBFindIn{Scope: "board", Query: "jump"}))
	if len(found.Pages) != 1 || found.Pages[0].Freshness.Status != kb.Unknown {
		t.Errorf("board-wide find = %+v", found.Pages)
	}
}

// A path source with no file in the working copy (a typo, QM-37) leaves no git diff, but must not leave the page
// fresh: the page is stale, names the source, and the write says so. A glob that matches files is fine.
func TestMissingSourceIsNotFresh(t *testing.T) {
	f := newFixture(t)
	repo, _ := gitRepo(t, f)
	a := Caller{ID: "claude-code:aaaa", Cwd: repo}
	text, out := must[PageOut](t)(f.svc.KBWrite(a, KBWriteIn{Project: "rpg", Title: "Typo", Body: "x",
		Sources: []string{"Source/Other.cpp", "Source/Collision/*.cpp", "Source/Typo.cpp"}}))
	if out.Freshness.Status != kb.Stale || len(out.Freshness.Changed) != 1 || out.Freshness.Changed[0] != "Source/Typo.cpp"+kb.NotFound ||
		!strings.Contains(text, "not in your working copy: Source/Typo.cpp") {
		t.Errorf("write = %q %+v", text, out.Freshness)
	}
	_, out = must[PageOut](t)(f.svc.KBWrite(a, KBWriteIn{Page: out.Ref, Sources: []string{"Source/Other.cpp", "Source/Collision/*.cpp"}}))
	if out.Freshness.Status != kb.Fresh {
		t.Errorf("fixed sources: %+v", out.Freshness)
	}
}
