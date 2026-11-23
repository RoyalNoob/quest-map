package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
)

var (
	agent = board.Actor{Kind: "agent", Session: "claude-code:7f3a", Model: "claude-opus-5-5"}
	me    = board.Actor{Kind: "person", Name: "me"}
)

func ptr[T any](v T) *T { return &v }

// fixture builds a board with a few tasks and an index over it.
//
//	RPG-1 todo   agent code  labels physics,bug  asset /Game/Props/SM_Bridge_Mid  blocked by RPG-2
//	RPG-2 doing  agent code  claimed, usage by claude-opus-5-5
//	RPG-3 done   person art  "Paint the castle gate"
//	RPG-4 review agent audio blocked by RPG-3 (done, so not blocked)
func fixture(t *testing.T) (*board.Board, *board.Project, *Index) {
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
	p.Update("x", me, board.Edit{}, "", nil) // no-op on a missing task must not matter
	mk := func(tk *board.Task) *board.Task {
		got, err := p.Create(tk, me, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	mk(&board.Task{Title: "Player falls through bridge collision", Doer: board.Agent, Discipline: "code",
		Labels: []string{"physics", "bug"}, Asset: "/Game/Props/SM_Bridge_Mid", BlockedBy: []string{"RPG-2"},
		Body: "Repro: load L_Bridge and sprint across."})
	mk(&board.Task{Title: "Regenerate collision meshes", Doer: board.Agent, Discipline: "code"})
	mk(&board.Task{Title: "Paint the castle gate", Doer: board.Person, Discipline: "art"})
	mk(&board.Task{Title: "Footstep sounds on wood", Doer: board.Agent, Discipline: "audio", BlockedBy: []string{"RPG-3"}})
	if _, err := p.Claim("RPG-2", agent); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Note("RPG-2", agent, "regenerating", &board.Usage{Tokens: board.Tokens{Input: 1000, Output: 4000}, Time: 20 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Update("RPG-3", me, board.Edit{Status: ptr(board.Done)}, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Update("RPG-4", me, board.Edit{Status: ptr(board.Review)}, "", nil); err != nil {
		t.Fatal(err)
	}
	ix, err := Open(filepath.Join(root, ".cache", "index.sqlite"), b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return b, p, ix
}

func ids(t *testing.T, ix *Index, filter string) string {
	t.Helper()
	rows, err := ix.FindString(filter, FindOptions{})
	if err != nil {
		t.Fatalf("%q: %v", filter, err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return strings.Join(out, " ")
}

func TestFindFilters(t *testing.T) {
	_, _, ix := fixture(t)
	for filter, want := range map[string]string{
		"":                        "RPG-1 RPG-2 RPG-3 RPG-4",
		"status:doing":            "RPG-2",
		"-status:done":            "RPG-1 RPG-2 RPG-4",
		"doer:agent -status:done": "RPG-1 RPG-2 RPG-4",
		"discipline:CODE":         "RPG-1 RPG-2",
		"label:physics":           "RPG-1",
		"-label:physics":          "RPG-2 RPG-3 RPG-4",
		"asset:SM_Bridge*":        "RPG-1",
		"asset:/Game/Props/*":     "RPG-1",
		"asset:SM_Castle*":        "",
		"blocked:yes":             "RPG-1",
		"blocked:no":              "RPG-2 RPG-3 RPG-4",
		"spent:yes":               "RPG-2",
		"spent:no":                "RPG-1 RPG-3 RPG-4",
		"model:claude-opus*":      "RPG-2",
		"bridge":                  "RPG-1",
		"collis":                  "RPG-1 RPG-2",
		"collision -bridge":       "RPG-2",
		`"castle gate"`:           "RPG-3",
		"regenerating":            "RPG-2", // log text is searchable
		"priority:normal":         "RPG-1 RPG-2 RPG-3 RPG-4",
		"found:latest":            "",
		"stale:yes":               "",
	} {
		if got := ids(t, ix, filter); got != want {
			t.Errorf("%q = %q, want %q", filter, got, want)
		}
	}
}

func TestStaleClaims(t *testing.T) {
	_, _, ix := fixture(t)
	ix.opts.Now = func() time.Time { return time.Now().Add(5 * time.Hour) }
	if got := ids(t, ix, "stale:yes"); got != "RPG-2" {
		t.Errorf("stale:yes = %q", got)
	}
}

func TestSpentAndRowFields(t *testing.T) {
	_, _, ix := fixture(t)
	rows, _ := ix.FindString("status:doing", FindOptions{})
	r := rows[0]
	if r.SpentTokens != 5000 || r.SpentTime != 20*time.Minute || r.SpentRecords != 1 || r.SpentSelf != 1 {
		t.Errorf("spent = %d %s %d %d", r.SpentTokens, r.SpentTime, r.SpentRecords, r.SpentSelf)
	}
	if r.ClaimBy != "claude-code:7f3a" || r.ClaimAt.IsZero() || r.Updated.IsZero() {
		t.Errorf("claim/updated = %q %v %v", r.ClaimBy, r.ClaimAt, r.Updated)
	}
	rows, _ = ix.FindString("label:physics", FindOptions{})
	if strings.Join(rows[0].Labels, ",") != "physics,bug" || strings.Join(rows[0].BlockedBy, ",") != "RPG-2" {
		t.Errorf("lists = %v %v", rows[0].Labels, rows[0].BlockedBy)
	}
}

func TestArchivedAreHiddenByDefault(t *testing.T) {
	_, p, ix := fixture(t)
	if err := p.Archive("RPG-3", me); err != nil {
		t.Fatal(err)
	}
	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got := ids(t, ix, ""); got != "RPG-1 RPG-2 RPG-4" {
		t.Errorf("default = %q", got)
	}
	rows, _ := ix.FindString("", FindOptions{IncludeArchived: true})
	if len(rows) != 4 {
		t.Errorf("with archive: %d rows", len(rows))
	}
	// An archived blocker counts as done.
	if got := ids(t, ix, "blocked:yes"); got != "RPG-1" {
		t.Errorf("blocked:yes = %q", got)
	}
}

func TestRefreshFileSkipsEchoes(t *testing.T) {
	_, p, ix := fixture(t)
	path := p.TaskPath("RPG-1", false)
	changed, err := ix.RefreshFile(path)
	if err != nil || changed {
		t.Errorf("unchanged file: changed=%v err=%v", changed, err)
	}
	p.Note("RPG-1", me, "a note", nil)
	if changed, _ := ix.RefreshFile(path); !changed {
		t.Error("changed file not refreshed")
	}
	if changed, _ := ix.RefreshFile(path); changed {
		t.Error("second refresh of the same content changed the index")
	}
}

func TestParseErrorKeepsLastGoodRow(t *testing.T) {
	_, p, ix := fixture(t)
	path := p.TaskPath("RPG-1", false)
	good, _ := os.ReadFile(path)
	broken := strings.Replace(string(good), "status: todo", "status: [oops", 1)
	os.WriteFile(path, []byte(broken), 0o644)
	if _, err := ix.RefreshFile(path); err != nil {
		t.Fatal(err)
	}
	rows, _ := ix.FindString("label:physics", FindOptions{})
	if len(rows) != 1 || rows[0].ParseError == "" || rows[0].Title != "Player falls through bridge collision" {
		t.Fatalf("rows = %+v", rows)
	}
	probs, _ := ix.ParseProblems()
	if len(probs) != 1 || probs[0].ID != "RPG-1" {
		t.Errorf("problems = %+v", probs)
	}
	// quest-map never overwrites the broken file.
	if _, err := p.Note("RPG-1", agent, "try", nil); err == nil {
		t.Error("note on a broken file succeeded")
	}
	if now, _ := os.ReadFile(path); string(now) != broken {
		t.Error("broken file was changed")
	}
	// Fixed by hand: the flag goes away.
	os.WriteFile(path, good, 0o644)
	ix.RefreshFile(path)
	if probs, _ := ix.ParseProblems(); len(probs) != 0 {
		t.Errorf("problems after fix = %+v", probs)
	}
	// A new file that never parsed is reported without a row.
	bad := filepath.Join(filepath.Dir(path), "RPG-99.md")
	os.WriteFile(bad, []byte("no front matter"), 0o644)
	ix.RefreshFile(bad)
	if probs, _ := ix.ParseProblems(); len(probs) != 1 || probs[0].ID != "" {
		t.Errorf("problems = %+v", probs)
	}
}

func TestDuplicateIDsAreRenumbered(t *testing.T) {
	b, p, ix := fixture(t)
	// A copy of RPG-1 under another name (as left by resolving a sync conflict), created later.
	data, _ := os.ReadFile(p.TaskPath("RPG-1", false))
	copyData := strings.Replace(string(data), "title: Player falls through bridge collision", "title: Bridge copy from laptop", 1)
	copyData = strings.Replace(copyData, "created: 2026", "created: 2027", 1)
	if copyData == string(data) || !strings.Contains(copyData, "created: 2027") {
		// created year depends on the clock; set it explicitly
		copyData = replaceCreated(t, copyData, "2099-01-01T00:00:00Z")
	}
	dup := filepath.Join(filepath.Dir(p.TaskPath("RPG-1", false)), "RPG-1 (laptop).md")
	os.WriteFile(dup, []byte(copyData), 0o644)

	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	rs := ix.Renumbered()
	if len(rs) != 1 || rs[0].OldID != "RPG-1" || rs[0].NewID != "RPG-5" {
		t.Fatalf("renumbered = %+v", rs)
	}
	// The later-created copy keeps RPG-1 and moves to the canonical file name; the other becomes RPG-5.
	kept, err := p.Get("RPG-1")
	if err != nil || kept.Title != "Bridge copy from laptop" {
		t.Fatalf("RPG-1 = %+v %v", kept, err)
	}
	moved, err := p.Get("RPG-5")
	if err != nil || moved.MovedFrom != "RPG-1" || moved.Title != "Player falls through bridge collision" {
		t.Fatalf("RPG-5 = %+v %v", moved, err)
	}
	if !strings.Contains(moved.Log[len(moved.Log)-1].Text, "renumbered from RPG-1") ||
		!strings.Contains(kept.Log[len(kept.Log)-1].Text, "renumbered to RPG-5") {
		t.Errorf("log lines: %q / %q", moved.Log[len(moved.Log)-1].Text, kept.Log[len(kept.Log)-1].Text)
	}
	if _, err := os.Stat(dup); !os.IsNotExist(err) {
		t.Error("duplicate file still there")
	}
	if got := ids(t, ix, "label:physics"); got != "RPG-1 RPG-5" {
		t.Errorf("index = %q", got)
	}
	_ = b
}

func replaceCreated(t *testing.T, s, created string) string {
	t.Helper()
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "created: ") {
			lines[i] = "created: " + created
			return strings.Join(lines, "\n")
		}
	}
	t.Fatal("no created line")
	return s
}

// TestWatcherPicksUpHandEdits is the M2 acceptance check: a hand edit shows up in queries within a second.
func TestWatcherPicksUpHandEdits(t *testing.T) {
	_, p, ix := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var changes []Change
	w, err := ix.Watch(ctx, func(c Change) { mu.Lock(); changes = append(changes, c); mu.Unlock() }, func(err error) { t.Log("watch:", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); <-w.Done() }()

	waitFor := func(what, filter, want string) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			got := ids(t, ix, filter)
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %q = %q after 1s, want %q", what, filter, got, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Edit a title by hand, the way an editor would (several writes in a row).
	path := p.TaskPath("RPG-3", false)
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), "Paint the castle gate", "Paint the drawbridge", 1)
	os.WriteFile(path, []byte(edited[:len(edited)/2]), 0o644)
	os.WriteFile(path, []byte(edited), 0o644)
	waitFor("hand edit", "drawbridge", "RPG-3")

	// A task file added by hand.
	newTask := "---\nid: RPG-20\ntitle: Hand-made task about lanterns\nstatus: todo\n---\n"
	os.WriteFile(filepath.Join(filepath.Dir(path), "RPG-20.md"), []byte(newTask), 0o644)
	waitFor("new file", "lanterns", "RPG-20")

	// Deleted by hand.
	os.Remove(filepath.Join(filepath.Dir(path), "RPG-20.md"))
	waitFor("deleted file", "lanterns", "")

	// A write through the board (as the server does) and usage in activity.jsonl.
	p.Note("RPG-4", agent, "", &board.Usage{Tokens: board.Tokens{Total: 900}})
	waitFor("activity", "spent:yes", "RPG-2 RPG-4")

	mu.Lock()
	n := len(changes)
	mu.Unlock()
	if n == 0 {
		t.Error("no change callbacks")
	}
}
