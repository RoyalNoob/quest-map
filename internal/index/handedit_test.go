package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// ownRecords returns the activity records this board copy wrote after the ids in before, as "kind:detail".
func ownRecords(t *testing.T, b *board.Board, p *board.Project, before map[string]bool) []string {
	t.Helper()
	recs, _, err := p.Activity()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		if before[r.ID] || r.Origin != b.Origin() {
			continue
		}
		switch r.Kind {
		case board.KindStatus:
			out = append(out, "status:"+r.From+">"+r.To)
		case board.KindNote:
			out = append(out, "note:"+r.Text)
		case board.KindEdit:
			out = append(out, "edit:"+strings.Join(r.Fields, ","))
		default:
			out = append(out, r.Kind)
		}
	}
	sort.Strings(out)
	return out
}

func recordIDs(t *testing.T, p *board.Project) map[string]bool {
	t.Helper()
	recs, _, err := p.Activity()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range recs {
		ids[r.ID] = true
	}
	return ids
}

// editFile rewrites a task file by hand: each pair in repl is replaced once, and lines are appended to the log.
func editFile(t *testing.T, path string, logLines []string, repl ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for i := 0; i+1 < len(repl); i += 2 {
		if !strings.Contains(s, repl[i]) {
			t.Fatalf("%s has no %q:\n%s", path, repl[i], s)
		}
		s = strings.Replace(s, repl[i], repl[i+1], 1)
	}
	for _, l := range logLines {
		s = strings.TrimRight(s, "\n") + "\n- 2026-11-18 10:00 pat: " + l + "\n"
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// appendForeign appends records another board copy (origin "laptop") wrote, as a pull brings them.
func appendForeign(t *testing.T, p *board.Project, recs ...board.Record) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(p.Dir(), "activity.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i, r := range recs {
		r.ID = "7ZZZZZZZZZZZZZZZZZZZZZZZZ" + string(rune('A'+i))
		r.At = time.Now().UTC()
		r.Origin = "laptop"
		r.Actor = "person"
		line, _ := json.Marshal(r)
		f.Write(append(line, '\n'))
	}
}

func withWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := HandEditWait
	HandEditWait = d
	t.Cleanup(func() { HandEditWait = old })
}

func TestHandEditIsRecordedAfterTheWait(t *testing.T) {
	withWait(t, 100*time.Millisecond)
	b, p, ix := fixture(t)
	before := recordIDs(t, p)
	path := p.TaskPath("RPG-1", false)
	editFile(t, path, []string{"checked on console too"}, "status: todo", "status: doing", "title: Player falls", "title: Hero falls")
	if _, err := ix.RefreshFile(path); err != nil {
		t.Fatal(err)
	}
	if got := ownRecords(t, b, p, before); len(got) != 0 {
		t.Fatalf("recorded before the wait: %v", got)
	}
	want := "edit:title note:checked on console too status:todo>doing"
	deadline := time.Now().Add(2 * time.Second)
	for strings.Join(ownRecords(t, b, p, before), " ") != want {
		if time.Now().After(deadline) {
			t.Fatalf("records = %v, want %s", ownRecords(t, b, p, before), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPulledChangeIsNotAHandEdit(t *testing.T) {
	foreign := func(p *board.Project) []board.Record {
		id := "RPG-1"
		return []board.Record{
			{Kind: board.KindStatus, Task: &id, From: "todo", To: "doing"},
			{Kind: board.KindNote, Task: &id, Text: "picked up on the laptop"},
		}
	}
	for _, order := range []string{"task first", "activity first"} {
		t.Run(order, func(t *testing.T) {
			withWait(t, time.Hour) // decided by FlushHandEdits only
			b, p, ix := fixture(t)
			before := recordIDs(t, p)
			path := p.TaskPath("RPG-1", false)
			pullTask := func() {
				// The title change has no record of its own (as with a claim field): only the origin tells.
				editFile(t, path, []string{"picked up on the laptop"}, "status: todo", "status: doing", "title: Player falls", "title: Hero falls")
				if _, err := ix.RefreshFile(path); err != nil {
					t.Fatal(err)
				}
			}
			pullActivity := func() {
				appendForeign(t, p, foreign(p)...)
				if err := ix.RefreshActivity("rpg"); err != nil {
					t.Fatal(err)
				}
			}
			if order == "task first" {
				pullTask()
				pullActivity()
			} else {
				pullActivity()
				pullTask()
			}
			if err := ix.FlushHandEdits(); err != nil {
				t.Fatal(err)
			}
			if got := ownRecords(t, b, p, before); len(got) != 0 {
				t.Errorf("pull recorded as a hand edit: %v", got)
			}
			if got := ids(t, ix, "status:doing"); got != "RPG-1 RPG-2" {
				t.Errorf("index not updated: %q", got)
			}
		})
	}
}

// A task file pulled before its activity lines, with the activity not even refreshed by the watcher yet:
// the decision reads the activity file from disk.
func TestPulledChangeActivityOnDiskOnly(t *testing.T) {
	withWait(t, time.Hour)
	b, p, ix := fixture(t)
	before := recordIDs(t, p)
	path := p.TaskPath("RPG-4", false)
	editFile(t, path, nil, "priority: normal", "priority: urgent")
	if _, err := ix.RefreshFile(path); err != nil {
		t.Fatal(err)
	}
	id := "RPG-4"
	appendForeign(t, p, board.Record{Kind: board.KindEdit, Task: &id, Fields: []string{"priority"}})
	if err := ix.FlushHandEdits(); err != nil {
		t.Fatal(err)
	}
	if got := ownRecords(t, b, p, before); len(got) != 0 {
		t.Errorf("pull recorded as a hand edit: %v", got)
	}
}

func TestOldForeignActivityDoesNotHideALaterHandEdit(t *testing.T) {
	withWait(t, 50*time.Millisecond)
	b, p, ix := fixture(t)
	id := "RPG-1"
	appendForeign(t, p, board.Record{Kind: board.KindNote, Task: &id, Text: "an old remark from the laptop"})
	if err := ix.RefreshActivity("rpg"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond) // longer ago than HandEditWait
	before := recordIDs(t, p)
	path := p.TaskPath("RPG-1", false)
	editFile(t, path, nil, "title: Player falls", "title: Hero falls")
	if _, err := ix.RefreshFile(path); err != nil {
		t.Fatal(err)
	}
	if err := ix.FlushHandEdits(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ownRecords(t, b, p, before), " "); got != "edit:title" {
		t.Errorf("records = %q", got)
	}
}

// Checking out an older commit takes records out of the activity file and puts an older task file back: that
// is not a person changing the status back.
func TestRewoundHistoryIsNotAHandEdit(t *testing.T) {
	for _, order := range []string{"task first", "activity first"} {
		t.Run(order, func(t *testing.T) {
			withWait(t, time.Hour)
			b, p, ix := fixture(t)
			path := p.TaskPath("RPG-1", false)
			actPath := filepath.Join(p.Dir(), "activity.jsonl")
			oldTask, _ := os.ReadFile(path)
			oldAct, _ := os.ReadFile(actPath)
			if _, err := p.Update("RPG-1", me, board.Edit{Status: ptr(board.Doing)}, "", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := ix.RefreshAfterWrite(path); err != nil {
				t.Fatal(err)
			}
			if err := ix.RefreshActivity("rpg"); err != nil {
				t.Fatal(err)
			}
			before := recordIDs(t, p)
			rewindTask := func() {
				os.WriteFile(path, oldTask, 0o644)
				if _, err := ix.RefreshFile(path); err != nil {
					t.Fatal(err)
				}
			}
			rewindAct := func() {
				os.WriteFile(actPath, oldAct, 0o644)
				if err := ix.RefreshActivity("rpg"); err != nil {
					t.Fatal(err)
				}
			}
			if order == "task first" {
				rewindTask()
				rewindAct()
			} else {
				rewindAct()
				rewindTask()
			}
			if err := ix.FlushHandEdits(); err != nil {
				t.Fatal(err)
			}
			if got := ownRecords(t, b, p, before); len(got) != 0 {
				t.Errorf("rewind recorded as a hand edit: %v", got)
			}
		})
	}
}

func TestFixingABrokenFileRecordsTheEdit(t *testing.T) {
	withWait(t, time.Hour)
	b, p, ix := fixture(t)
	before := recordIDs(t, p)
	path := p.TaskPath("RPG-1", false)
	good, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(good), "status: todo", "status: [oops", 1)), 0o644)
	if _, err := ix.RefreshFile(path); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(strings.Replace(string(good), "status: todo", "status: review", 1)), 0o644)
	if _, err := ix.RefreshFile(path); err != nil {
		t.Fatal(err)
	}
	if err := ix.FlushHandEdits(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ownRecords(t, b, p, before), " "); got != "status:todo>review" {
		t.Errorf("records = %q", got)
	}
}

func TestFailedIndexWriteRecordsNothing(t *testing.T) {
	withWait(t, 0) // would record at once
	b, p, ix := fixture(t)
	before := recordIDs(t, p)
	path := p.TaskPath("RPG-1", false)
	editFile(t, path, nil, "status: todo", "status: doing")
	if _, err := ix.db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := ix.RefreshFile(path); err == nil {
			t.Fatal("refresh succeeded")
		}
	}
	if got := ownRecords(t, b, p, before); len(got) != 0 {
		t.Fatalf("records written although the index write failed: %v", got)
	}
	// Once the index can be written, the change is recorded once.
	if _, err := ix.db.Exec(`DROP TRIGGER fail_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.RefreshFile(path); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ownRecords(t, b, p, before), " "); got != "status:todo>doing" {
		t.Errorf("records = %q", got)
	}
}

func TestEditsWhileWaitingAreRecordedOnce(t *testing.T) {
	withWait(t, time.Hour)
	b, p, ix := fixture(t)
	before := recordIDs(t, p)
	path := p.TaskPath("RPG-1", false)
	editFile(t, path, nil, "status: todo", "status: doing")
	ix.RefreshFile(path)
	editFile(t, path, nil, "status: doing", "status: review")
	ix.RefreshFile(path)
	if err := ix.FlushHandEdits(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ownRecords(t, b, p, before), " "); got != "status:todo>review" {
		t.Errorf("records = %q", got)
	}
}

func TestOwnWriteDecidesAWaitingHandEdit(t *testing.T) {
	withWait(t, time.Hour)
	b, p, ix := fixture(t)
	before := recordIDs(t, p)
	path := p.TaskPath("RPG-1", false)
	editFile(t, path, nil, "title: Player falls", "title: Hero falls")
	ix.RefreshFile(path)
	if _, err := p.Note("RPG-1", agent, "looking", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.RefreshAfterWrite(path); err != nil {
		t.Fatal(err)
	}
	got := ownRecords(t, b, p, before)
	if strings.Join(got, " ") != "edit:title note:looking" {
		t.Errorf("records = %v", got)
	}
}

func TestRebuildDropsWaitingHandEdits(t *testing.T) {
	withWait(t, time.Hour)
	b, p, ix := fixture(t)
	before := recordIDs(t, p)
	path := p.TaskPath("RPG-1", false)
	editFile(t, path, nil, "status: todo", "status: doing")
	ix.RefreshFile(path)
	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if err := ix.FlushHandEdits(); err != nil {
		t.Fatal(err)
	}
	if got := ownRecords(t, b, p, before); len(got) != 0 {
		t.Errorf("records after a rebuild = %v", got)
	}
}
