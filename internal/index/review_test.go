package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// taskFile returns a minimal task file.
func taskFile(id, title, created string) string {
	return "---\nid: " + id + "\ntitle: " + title + "\nstatus: todo\ncreated: " + created + "\n---\n"
}

func TestLiveDuplicateKeeperGetsCanonicalName(t *testing.T) {
	_, p, ix := fixture(t)
	odd := filepath.Join(p.Dir(), "tasks", "RPG-9.md")
	os.WriteFile(odd, []byte(taskFile("RPG-1", "newer copy", "2099-01-01T00:00:00Z")), 0o644)
	if _, err := ix.RefreshFile(odd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(odd); !os.IsNotExist(err) {
		t.Error("RPG-9.md still there")
	}
	kept, err := p.Get("RPG-1")
	if err != nil || kept.Title != "newer copy" {
		t.Fatalf("RPG-1 = %+v %v", kept, err)
	}
	rs := ix.Renumbered()
	if len(rs) != 1 {
		t.Fatalf("renumbered = %+v", rs)
	}
	moved, err := p.Get(rs[0].NewID) // RPG-10: the file name RPG-9.md counted as used
	if err != nil || moved.MovedFrom != "RPG-1" || moved.Title != "Player falls through bridge collision" {
		t.Fatalf("%s = %+v %v", rs[0].NewID, moved, err)
	}
	if got := ids(t, ix, "newer"); got != "RPG-1" {
		t.Errorf("index has %q for the kept copy", got)
	}
}

func TestDuplicateAfterOtherFileChangedIDIsNotRenumbered(t *testing.T) {
	_, p, ix := fixture(t)
	// A checkout changes RPG-1.md to RPG-9 and adds a new file with RPG-1; the new file's event comes first.
	os.WriteFile(p.TaskPath("RPG-1", false), []byte(taskFile("RPG-9", "was one", "2026-01-01T00:00:00Z")), 0o644)
	added := filepath.Join(p.Dir(), "tasks", "added.md")
	os.WriteFile(added, []byte(taskFile("RPG-1", "new one", "2026-01-02T00:00:00Z")), 0o644)
	if _, err := ix.RefreshFile(added); err != nil {
		t.Fatal(err)
	}
	if rs := ix.Renumbered(); len(rs) != 0 {
		t.Errorf("renumbered %+v", rs)
	}
	nine, err := p.Get("RPG-9")
	if err != nil || nine.MovedFrom != "" || nine.Title != "was one" {
		t.Errorf("RPG-9 = %+v %v", nine, err)
	}
	one, err := p.Get("RPG-1")
	if err != nil || one.Title != "new one" {
		t.Errorf("RPG-1 = %+v %v", one, err)
	}
	if got := ids(t, ix, `"was one"`) + "|" + ids(t, ix, `"new one"`); got != "RPG-9|RPG-1" {
		t.Errorf("index = %s", got)
	}
}

func TestDuplicateOfBrokenFileKeepsItsRow(t *testing.T) {
	_, p, ix := fixture(t)
	path := p.TaskPath("RPG-1", false)
	os.WriteFile(path, []byte("---\nid: RPG-1\ntitle: [broken\n---\n"), 0o644)
	ix.RefreshFile(path)
	cp := filepath.Join(p.Dir(), "tasks", "copy.md")
	os.WriteFile(cp, []byte(taskFile("RPG-1", "a copy", "2099-01-01T00:00:00Z")), 0o644)
	if _, err := ix.RefreshFile(cp); err != nil {
		t.Fatal(err)
	}
	if got := ids(t, ix, "label:physics"); got != "RPG-1" {
		t.Errorf("last good row of RPG-1 lost: %q", got)
	}
	probs, _ := ix.ParseProblems()
	var msgs []string
	for _, pr := range probs {
		msgs = append(msgs, filepath.Base(pr.Path)+": "+pr.Err)
	}
	if all := strings.Join(msgs, "\n"); !strings.Contains(all, "copy.md: id RPG-1 is also used by") || !strings.Contains(all, "RPG-1.md:") {
		t.Errorf("problems:\n%s", all)
	}
}

func TestSameBrokenContentIsNotAChange(t *testing.T) {
	_, p, ix := fixture(t)
	for _, path := range []string{p.TaskPath("RPG-1", false), filepath.Join(p.Dir(), "tasks", "new.md")} {
		os.WriteFile(path, []byte("no front matter"), 0o644)
		if changed, _ := ix.RefreshFile(path); !changed {
			t.Errorf("%s: first broken read not reported", filepath.Base(path))
		}
		if changed, _ := ix.RefreshFile(path); changed {
			t.Errorf("%s: same broken content reported again", filepath.Base(path))
		}
	}
}

func TestRemovedProjectFolderLeavesNoRows(t *testing.T) {
	b, _, ix := fixture(t)
	art, _ := b.CreateProject("art", "ART", "Art")
	art.Create(&board.Task{Title: "Sky texture"}, me, nil)
	ix.Rebuild()
	if got := ids(t, ix, "sky"); got != "ART-1" {
		t.Fatalf("before: %q", got)
	}
	os.RemoveAll(art.Dir())
	if err := ix.RefreshGone(art.Dir()); err != nil {
		t.Fatal(err)
	}
	if got := ids(t, ix, "sky"); got != "" {
		t.Errorf("after removal: %q", got)
	}
	if _, err := b.Project("art"); err == nil {
		t.Error("board still knows the removed project")
	}
}

func TestJapaneseAndShortWordSearch(t *testing.T) {
	_, p, ix := fixture(t)
	p.Create(&board.Task{Title: "橋の当たり判定がおかしい", Body: "L_Bridge で発生"}, me, nil)
	ix.Rebuild()
	for q, want := range map[string]string{"当たり判定": "RPG-5", "橋": "RPG-5", "発生": "RPG-5", "falls": "RPG-1", "SM": ""} {
		if got := ids(t, ix, q); got != want {
			t.Errorf("%q = %q, want %q", q, got, want)
		}
	}
}

func TestSpentYesCountsSkippedSelfReports(t *testing.T) {
	_, p, ix := fixture(t)
	tk, _ := p.Create(&board.Task{Title: "self only"}, me, nil)
	p.RecordUsage("", agent, &board.Usage{Tokens: board.Tokens{Input: 5}, Source: board.SourceMeasured})
	p.Note(tk.ID, agent, "guess", &board.Usage{Time: time.Hour}) // skipped: the session is measured
	ix.Rebuild()
	if got := ids(t, ix, "spent:yes self"); got != tk.ID {
		t.Errorf("spent:yes = %q", got)
	}
	rows, _ := ix.FindString("self", FindOptions{})
	if len(rows) != 1 || rows[0].SpentRecords != 0 || rows[0].SpentTime != 0 {
		t.Errorf("row = %+v", rows)
	}
}
