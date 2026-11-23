package board

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clockFrom returns a clock that starts at start and advances one minute per call.
func clockFrom(start time.Time) func() time.Time {
	t := start
	return func() time.Time {
		t = t.Add(time.Minute)
		return t
	}
}

// copyBoard copies a board folder, leaving out .run (machine-local, never synced).
func copyBoard(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if rel == ".run" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRenumberSplitsActivityByMachine(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	desk, err := Open(rootA)
	if err != nil {
		t.Fatal(err)
	}
	desk.Now = clockFrom(time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	pd, _ := desk.CreateProject("rpg", "RPG", "RPG")
	copyBoard(t, rootA, rootB) // the board is synced to a laptop before either creates a task
	laptop, err := Open(rootB)
	if err != nil {
		t.Fatal(err)
	}
	laptop.Now = clockFrom(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	if desk.Origin() == laptop.Origin() || desk.Origin() == "" {
		t.Fatalf("origins %q / %q", desk.Origin(), laptop.Origin())
	}
	pl, _ := laptop.Project("rpg")

	// Offline, both machines create RPG-1 and work on it.
	a, _ := pd.Create(&Task{Title: "desk task"}, roy, &Usage{Time: 10 * time.Minute})
	pd.Note(a.ID, agentA, "desk work", &Usage{Tokens: Tokens{Total: 100}})
	b, _ := pl.Create(&Task{Title: "laptop task"}, roy, &Usage{Time: 30 * time.Minute})
	pl.Note(b.ID, agentA, "laptop work", &Usage{Tokens: Tokens{Total: 5000}})
	if a.ID != "RPG-1" || b.ID != "RPG-1" {
		t.Fatalf("ids %s %s", a.ID, b.ID)
	}

	// git merges the laptop's work into the desk copy: activity lines are unioned, and the laptop's task file
	// arrives under another name.
	f, _ := os.OpenFile(pd.activityPath(), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(readFile(t, pl.activityPath()))
	f.Close()
	laptopFile := filepath.Join(pd.Dir(), "tasks", "RPG-1 (laptop).md")
	os.WriteFile(laptopFile, []byte(readFile(t, pl.taskPath("RPG-1", false))), 0o644)

	// The later-created copy (the laptop's) keeps RPG-1.
	newID, err := desk.Renumber(pd, pd.taskPath("RPG-1", false), pd, laptopFile, Actor{Name: "quest-map"})
	if err != nil || newID != "RPG-2" {
		t.Fatalf("renumber = %q %v", newID, err)
	}
	if _, err := pd.Adopt(laptopFile, "RPG-1"); err != nil {
		t.Fatal(err)
	}
	kept, _ := pd.Get("RPG-1")
	moved, _ := pd.Get("RPG-2")
	if kept == nil || kept.Title != "laptop task" || moved == nil || moved.Title != "desk task" || moved.MovedFrom != "RPG-1" {
		t.Fatalf("kept %+v moved %+v", kept, moved)
	}
	if !strings.Contains(moved.Log[len(moved.Log)-1].Text, "came along") {
		t.Errorf("renumbered log = %q", moved.Log[len(moved.Log)-1].Text)
	}

	sk, _ := pd.Spent("RPG-1")
	sm, _ := pd.Spent("RPG-2")
	if sk.Time != 30*time.Minute || sk.Tokens.Sum() != 5000 {
		t.Errorf("kept spent = %s %d", sk.Time, sk.Tokens.Sum())
	}
	if sm.Time != 10*time.Minute || sm.Tokens.Sum() != 100 {
		t.Errorf("renumbered spent = %s %d", sm.Time, sm.Tokens.Sum())
	}
	// Work on RPG-1 after the merge, on either machine, stays with RPG-1.
	pd.Note("RPG-1", agentA, "after the merge", &Usage{Tokens: Tokens{Total: 7}})
	if sk, _ = pd.Spent("RPG-1"); sk.Tokens.Sum() != 5007 {
		t.Errorf("kept spent after merge = %d", sk.Tokens.Sum())
	}
}

func TestRenumberWithoutOriginsKeepsActivity(t *testing.T) {
	_, p := testBoard(t)
	a, _ := p.Create(&Task{Title: "one"}, roy, &Usage{Time: 10 * time.Minute})
	// A hand-made copy on the same machine: both copies have the same origin, so nothing can be split.
	data := strings.Replace(readFile(t, p.taskPath(a.ID, false)), "created: 2026-11-17T09:01:00Z", "created: 2026-11-18T09:00:00Z", 1)
	cp := filepath.Join(p.Dir(), "tasks", "copy.md")
	os.WriteFile(cp, []byte(data), 0o644)
	newID, err := p.board.Renumber(p, p.taskPath(a.ID, false), p, cp, Actor{Name: "quest-map"})
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := p.Spent(a.ID)
	s2, _ := p.Spent(newID)
	if s1.Time != 10*time.Minute || s2.Time != 0 {
		t.Errorf("spent kept %s renumbered %s", s1.Time, s2.Time)
	}
	got, _ := os.ReadFile(cp)
	if !strings.Contains(string(got), "stays here") {
		t.Errorf("kept file log:\n%s", got)
	}
}

func TestMeasuredRuleIsPerActivityFile(t *testing.T) {
	b, rpg := testBoard(t)
	art, _ := b.CreateProject("art", "ART", "Art")
	at, _ := art.Create(&Task{Title: "art"}, roy, nil)
	rt, _ := rpg.Create(&Task{Title: "code"}, roy, nil)
	art.Note(at.ID, agentA, "measured", &Usage{Tokens: Tokens{Input: 10}, Source: SourceMeasured})
	rpg.Note(rt.ID, agentA, "self", &Usage{Tokens: Tokens{Total: 99}})
	// The session is measured in art's file only, so its self-report in rpg's file counts (§3.6).
	if s, _ := rpg.Spent(rt.ID); s.Tokens.Sum() != 99 || s.Records != 1 {
		t.Errorf("rpg spent = %+v", s)
	}
	l, _ := b.Ledger()
	if s := l.Spent[at.ID]; s.Tokens.Sum() != 10 {
		t.Errorf("art spent = %+v", s)
	}
}

func TestOpenKeepsUsersGitFiles(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.png binary"), 0o644)
	if _, err := Open(root); err != nil {
		t.Fatal(err)
	}
	ga := readFile(t, filepath.Join(root, ".gitattributes"))
	if !strings.Contains(ga, "*.png binary\n") || !strings.Contains(ga, "activity.jsonl merge=union") {
		t.Errorf(".gitattributes = %q", ga)
	}
	gi := readFile(t, filepath.Join(root, ".gitignore"))
	if !strings.Contains(gi, ".run/") || !strings.Contains(gi, ".cache/") || !strings.Contains(gi, ".trash/") {
		t.Errorf(".gitignore = %q", gi)
	}
	// Opening again adds nothing.
	Open(root)
	if again := readFile(t, filepath.Join(root, ".gitattributes")); again != ga {
		t.Errorf("second open changed .gitattributes: %q", again)
	}
}
