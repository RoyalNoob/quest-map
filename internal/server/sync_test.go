package server

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/boardsync"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Two copies of a board kept in one git repository edit different tasks, sync, and see each other's changes
// without recording the pulled changes as hand edits; editing the same line leaves a conflict that the next
// sync keeps reporting until it is resolved (spec §3.5).
func TestSyncTwoCopies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	bare, rootA, rootB := t.TempDir(), t.TempDir(), t.TempDir()
	gitT(t, bare, "init", "--bare", "-q", "-b", "main")
	b, _ := board.Open(rootA)
	p, _ := b.CreateProject("rpg", "RPG", "RPG")
	p.Create(&board.Task{Title: "Bridge collision"}, board.Actor{Kind: "person", Name: "me"}, nil)
	gitT(t, rootA, "init", "-q", "-b", "main")
	for _, root := range []string{rootA} {
		gitT(t, root, "config", "user.name", "A")
		gitT(t, root, "config", "user.email", "a@example.invalid")
	}
	gitT(t, rootA, "add", "-A")
	gitT(t, rootA, "commit", "-q", "-m", "board")
	gitT(t, rootA, "remote", "add", "origin", bare)
	gitT(t, rootA, "push", "-q", "-u", "origin", "main")
	gitT(t, rootB, "clone", "-q", bare, ".")
	gitT(t, rootB, "config", "user.name", "B")
	gitT(t, rootB, "config", "user.email", "b@example.invalid")

	start := func(root string) (*Server, *browser) {
		s, err := Start(root, Options{Port: -1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s, newBrowser(t, s)
	}
	sA, brA := start(rootA)
	sB, brB := start(rootB)
	ctx := context.Background()

	var st boardsync.Status
	if brA.do("GET", "/api/sync", nil, &st); st.State != boardsync.StateOK || st.Upstream != "origin/main" {
		t.Fatalf("A status = %+v", st)
	}
	brA.do("POST", "/api/tasks", map[string]any{"project": "rpg", "title": "Gate mesh"}, nil) // RPG-2 on A
	brB.do("PATCH", "/api/tasks/RPG-1", map[string]any{"priority": "high"}, nil)              // RPG-1 on B
	countOwn := func(s *Server) int {
		sp, _ := s.Board.Project("rpg")
		recs, _, _ := sp.Activity()
		n := 0
		for _, r := range recs {
			if r.Origin == s.Board.Origin() {
				n++
			}
		}
		return n
	}
	ownA, ownB := countOwn(sA), countOwn(sB)
	for i, s := range []*Server{sA, sB, sA} {
		if got := s.syncer.Sync(ctx); got.State != boardsync.StateOK || got.Ahead != 0 {
			t.Fatalf("sync %d = %+v", i, got)
		}
	}
	waitFor := func(br *browser, id string, ok func(taskJSON) bool) {
		t.Helper()
		var tj taskJSON
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if br.do("GET", "/api/tasks/"+id, nil, &tj) == 200 && ok(tj) {
				return
			}
		}
		t.Fatalf("%s never arrived: %+v", id, tj)
	}
	waitFor(brA, "RPG-1", func(tj taskJSON) bool { return tj.Priority == board.High })
	waitFor(brB, "RPG-2", func(tj taskJSON) bool { return tj.Title == "Gate mesh" })
	sA.Index.FlushHandEdits()
	sB.Index.FlushHandEdits()
	if countOwn(sA) != ownA || countOwn(sB) != ownB {
		t.Errorf("pulled changes were recorded as hand edits: A %d → %d, B %d → %d", ownA, countOwn(sA), ownB, countOwn(sB))
	}

	// The same field on both copies: the second sync leaves a conflict and keeps saying so.
	brA.do("PATCH", "/api/tasks/RPG-1", map[string]any{"title": "Bridge collision (A)"}, nil)
	brB.do("PATCH", "/api/tasks/RPG-1", map[string]any{"title": "Bridge collision (B)"}, nil)
	sA.syncer.Sync(ctx)
	got := sB.syncer.Sync(ctx)
	if got.State != boardsync.StateConflict || len(got.Files) != 1 || !strings.HasSuffix(got.Files[0], "RPG-1.md") {
		t.Fatalf("conflict sync = %+v", got)
	}
	if again := sB.syncer.Sync(ctx); again.State != boardsync.StateConflict {
		t.Errorf("second sync during a conflict = %+v", again)
	}
	var w struct{ Sync string }
	if brB.do("GET", "/api/warnings", nil, &w); !strings.Contains(w.Sync, "RPG-1.md") {
		t.Errorf("warnings sync = %q", w.Sync)
	}
	// Resolving the file (without git add) is enough: the next sync finishes the merge and pushes.
	gitT(t, rootB, "checkout", "--theirs", "--", got.Files[0])
	if done := sB.syncer.Sync(ctx); done.State != boardsync.StateOK || done.Ahead != 0 {
		t.Fatalf("sync after resolving = %+v", done)
	}
	sA.syncer.Sync(ctx)
	waitFor(brA, "RPG-1", func(tj taskJSON) bool { return tj.Title == "Bridge collision (A)" })
}
