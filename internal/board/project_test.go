package board

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	agentA = Actor{Kind: "agent", Session: "claude-code:7f3a", Client: "claude-code/2.4.1", Model: "claude-opus-5-5"}
	agentB = Actor{Kind: "agent", Session: "codex:91bc", Client: "codex/1.0"}
	roy    = Actor{Kind: "person", Name: "roy"}
)

// testBoard opens a board in a temp folder with a clock that advances one minute per call.
func testBoard(t *testing.T) (*Board, *Project) {
	t.Helper()
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	b.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Minute)
		return clock
	}
	p, err := b.CreateProject("rpg", "RPG", "RPG")
	if err != nil {
		t.Fatal(err)
	}
	return b, p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func ptr[T any](v T) *T { return &v }

func TestCreateWritesCanonicalFile(t *testing.T) {
	_, p := testBoard(t)
	tk, err := p.Create(&Task{Title: "Player falls through bridge", Doer: Agent, Discipline: "code",
		Labels: []string{"physics", "bug"}, Body: "Repro: sprint across."}, roy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID != "RPG-1" {
		t.Fatalf("id = %s", tk.ID)
	}
	want := `---
id: RPG-1
title: Player falls through bridge
status: todo
doer: agent
assignees: []
claim: null
priority: normal
discipline: code
milestone: null
labels: [physics, bug]
asset: null
pipeline: null
found_in: null
fixed_in: null
blocked_by: []
moved_from: null
url: null
created: 2026-11-17T09:01:00Z
---

Repro: sprint across.

<!-- quest-map:log -->
## Log
- 2026-11-17 09:01 roy: created
`
	if got := readFile(t, p.taskPath("RPG-1", false)); got != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	// Reading and writing again changes nothing.
	again, _ := p.Get("RPG-1")
	out, _ := again.Render()
	if string(out) != want {
		t.Errorf("round trip changed the file:\n%s", out)
	}
}

func TestWritesDiffCleanly(t *testing.T) {
	_, p := testBoard(t)
	tk, _ := p.Create(&Task{Title: "Bridge", Doer: Agent}, roy, nil)
	path := p.taskPath(tk.ID, false)
	before := readFile(t, path)

	// A note only appends a line.
	if _, err := p.Note(tk.ID, agentA, "looking at the collision mesh", &Usage{Tokens: Tokens{Output: 4200}, Time: 25 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, path)
	if !strings.HasPrefix(after, before) {
		t.Fatalf("note changed existing lines:\n%s", after)
	}
	if added := strings.TrimPrefix(after, before); added != "- 2026-11-17 09:02 claude-code:7f3a (+4.2k tok, +25m): looking at the collision mesh\n" {
		t.Errorf("added %q", added)
	}

	// A status edit changes only the status line in the front matter, plus log lines.
	before = after
	if _, err := p.Update(tk.ID, roy, Edit{Status: ptr(Review)}, "", nil); err != nil {
		t.Fatal(err)
	}
	after = readFile(t, path)
	b, a := strings.Split(strings.TrimSuffix(before, "\n"), "\n"), strings.Split(after, "\n")
	var diffs []string
	for i := range b {
		if i < len(a) && a[i] != b[i] {
			diffs = append(diffs, b[i]+" → "+a[i])
		}
	}
	if len(diffs) != 1 || diffs[0] != "status: todo → status: review" {
		t.Errorf("changed lines: %q", diffs)
	}
	if !strings.HasSuffix(after, "- 2026-11-17 09:03 roy: status todo → review\n") {
		t.Errorf("status log line missing:\n%s", after)
	}
}

func TestCustomFields(t *testing.T) {
	_, p := testBoard(t)
	p.Create(&Task{Title: "Crash on door", Custom: map[string]string{"platform": "PS5"}}, roy, nil)
	path := p.taskPath("RPG-1", false)
	if got := readFile(t, path); !strings.Contains(got, "created: 2026-11-17T09:01:00Z\nplatform: PS5\n---") {
		t.Fatalf("new file:\n%s", got)
	}
	// A hand-added key with a comment, and a nested value that is not a custom field.
	data := strings.Replace(readFile(t, path), "platform: PS5\n", "platform: PS5 # from playtest\nue_map: /Game/Maps/L_Gate\nnotes: {a: 1}\n", 1)
	os.WriteFile(path, []byte(data), 0o644)
	tk, _ := p.Get("RPG-1")
	if tk.Custom["platform"] != "PS5" || tk.Custom["ue_map"] != "/Game/Maps/L_Gate" || len(tk.Custom) != 2 {
		t.Fatalf("custom = %v", tk.Custom)
	}
	if _, err := p.Update("RPG-1", roy, Edit{Custom: map[string]string{"platform": "Win64", "ue_map": "", "build_tag": "nightly"}}, "", nil); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	for _, want := range []string{"platform: Win64 # from playtest\n", "notes: {a: 1}\n", "build_tag: nightly\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ue_map") {
		t.Errorf("cleared ue_map still there:\n%s", got)
	}
	if _, err := p.Update("RPG-1", roy, Edit{Custom: map[string]string{"status": "done"}}, "", nil); err == nil {
		t.Error("a built-in key was accepted as a custom field")
	}
}

func TestLegacyAssigneeMovesToList(t *testing.T) {
	_, p := testBoard(t)
	p.Create(&Task{Title: "Rig"}, roy, nil)
	path := p.taskPath("RPG-1", false)
	// An early build wrote a single name under `assignee` and no `assignees` key.
	data := strings.Replace(readFile(t, path), "assignees: []\n", "assignee: Mike\n", 1)
	os.WriteFile(path, []byte(data), 0o644)
	tk, err := p.Get("RPG-1")
	if err != nil || strings.Join(tk.Assignees, ",") != "Mike" {
		t.Fatalf("read = %v %v", tk, err)
	}
	add := []string{"Mike", " Jo ", "jo", ""}
	if _, err := p.Update("RPG-1", roy, Edit{Assignees: &add}, "", nil); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "assignees: [Mike, Jo]\n") || strings.Contains(got, "assignee:") {
		t.Errorf("file:\n%s", got)
	}
}

func TestIDAllocation(t *testing.T) {
	_, p := testBoard(t)
	for i := 1; i <= 3; i++ {
		tk, err := p.Create(&Task{Title: fmt.Sprint("t", i)}, roy, nil)
		if err != nil || tk.ID != fmt.Sprintf("RPG-%d", i) {
			t.Fatalf("create %d: %v %v", i, tk, err)
		}
	}
	// A file from another machine with a higher number, and a stale next_id, never cause reuse.
	data := strings.Replace(readFile(t, p.taskPath("RPG-3", false)), "id: RPG-3", "id: RPG-10", 1)
	if err := os.WriteFile(p.taskPath("RPG-10", false), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tk, err := p.Create(&Task{Title: "next"}, roy, nil)
	if err != nil || tk.ID != "RPG-11" {
		t.Fatalf("got %v %v, want RPG-11", tk, err)
	}
	if p.Config().NextID != 12 {
		t.Errorf("next_id = %d", p.Config().NextID)
	}
}

func TestConcurrentCreatesGetUniqueIDs(t *testing.T) {
	_, p := testBoard(t)
	var wg sync.WaitGroup
	ids := make([]string, 20)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, err := p.Create(&Task{Title: "parallel"}, agentA, nil)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = tk.ID
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate id %s", id)
		}
		seen[id] = true
	}
}

func TestClaimReleaseAndClearing(t *testing.T) {
	_, p := testBoard(t)
	tk, _ := p.Create(&Task{Title: "claim me", Doer: Agent}, roy, nil)
	got, err := p.Claim(tk.ID, agentA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Doing || got.Claim == nil || got.Claim.By != "claude-code:7f3a" {
		t.Fatalf("after claim: %+v %+v", got.Status, got.Claim)
	}
	if _, err := p.Claim(tk.ID, agentB); err == nil || !strings.Contains(err.Error(), "claimed by claude-code:7f3a") {
		t.Errorf("second claim err = %v", err)
	}
	got, _ = p.Update(tk.ID, agentA, Edit{Status: ptr(Review)}, "ready", nil)
	if got.Claim != nil {
		t.Error("moving to review must clear the claim")
	}
	if _, err := p.Claim(tk.ID, agentB); err != nil {
		t.Fatalf("claim after review: %v", err)
	}
	got, _ = p.Release(tk.ID, agentB, "", nil)
	if got.Status != Todo || got.Claim != nil {
		t.Errorf("after release: %s %+v", got.Status, got.Claim)
	}
	if got.Log[len(got.Log)-1].Text != "released" {
		t.Errorf("last log = %+v", got.Log[len(got.Log)-1])
	}
	p.Update(tk.ID, roy, Edit{Status: ptr(Done)}, "", nil)
	if _, err := p.Claim(tk.ID, agentA); err == nil {
		t.Error("claimed a done task")
	}
}

func TestArchiveAndMove(t *testing.T) {
	b, rpg := testBoard(t)
	art, err := b.CreateProject("art", "ART", "Art")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := rpg.Create(&Task{Title: "finish me"}, roy, nil)
	if err := rpg.Archive(a.ID, roy); err == nil {
		t.Error("archived a todo task")
	}
	rpg.Update(a.ID, roy, Edit{Status: ptr(Done)}, "", nil)
	if err := rpg.Archive(a.ID, roy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rpg.taskPath(a.ID, false)); !os.IsNotExist(err) {
		t.Error("task file still in tasks/")
	}
	got, err := rpg.Get(a.ID)
	if err != nil || !got.Archived || got.Log[len(got.Log)-1].Text != "archived" {
		t.Fatalf("archived get: %+v %v", got, err)
	}
	if open, _ := rpg.List(); len(open) != 0 {
		t.Errorf("open tasks = %d", len(open))
	}
	if arch, _ := rpg.ListArchived(); len(arch) != 1 {
		t.Errorf("archived tasks = %d", len(arch))
	}

	m, _ := rpg.Create(&Task{Title: "an art task", Doer: Agent}, roy, nil)
	rpg.Claim(m.ID, agentA)
	att := filepath.Join(rpg.Dir(), "attachments", m.ID)
	os.MkdirAll(att, 0o755)
	os.WriteFile(filepath.Join(att, "shot.png"), []byte("png"), 0o644)

	moved, err := b.Move(rpg, m.ID, art, roy)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ID != "ART-1" || moved.MovedFrom != m.ID || moved.Claim != nil || moved.Status != Todo {
		t.Errorf("moved: id=%s from=%s claim=%v status=%s", moved.ID, moved.MovedFrom, moved.Claim, moved.Status)
	}
	if _, err := rpg.Get(m.ID); err == nil {
		t.Error("source task still exists")
	}
	if _, err := os.Stat(filepath.Join(art.Dir(), "attachments", "ART-1", "shot.png")); err != nil {
		t.Errorf("attachment not moved: %v", err)
	}
	back, _ := art.Get("ART-1")
	if back.MovedFrom != "RPG-2" || !strings.Contains(readFile(t, art.taskPath("ART-1", false)), "id: ART-1") {
		t.Errorf("moved file: %+v", back)
	}
}

func TestUsageSums(t *testing.T) {
	_, p := testBoard(t)
	tk, _ := p.Create(&Task{Title: "spend"}, roy, &Usage{Time: 5 * time.Minute, Source: SourceHuman})
	p.Note(tk.ID, agentB, "tokens only", &Usage{Tokens: Tokens{Total: 10_000}})
	p.Note(tk.ID, agentB, "time only", &Usage{Time: 30 * time.Minute})
	p.Note(tk.ID, agentA, "split", &Usage{Tokens: Tokens{Input: 1000, Output: 2000, CacheRead: 30_000, CacheCreation: 500}, Time: 10 * time.Minute, Source: SourceMeasured})
	p.Note(tk.ID, agentA, "self report from a measured session is ignored", &Usage{Tokens: Tokens{Total: 999_999}, Time: time.Hour})
	p.Note(tk.ID, roy, "correction", &Usage{Time: -5 * time.Minute, Source: SourceHuman})

	s, err := p.Spent(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Tokens.Sum() != 10_000+33_500 || s.Tokens.CacheRead != 30_000 {
		t.Errorf("tokens = %+v", s.Tokens)
	}
	if s.Time != 40*time.Minute {
		t.Errorf("time = %s", s.Time)
	}
	if s.Records != 5 || s.Self != 2 {
		t.Errorf("records = %d self = %d", s.Records, s.Self)
	}
	if m := s.Models["claude-opus-5-5"]; m.Sum() != 33_500 {
		t.Errorf("per model = %+v", s.Models)
	}
	// Usage not tied to a task counts for the project only.
	if err := p.RecordUsage("", agentB, &Usage{Tokens: Tokens{Total: 7}}); err != nil {
		t.Fatal(err)
	}
	recs, _, _ := p.Activity()
	if last := recs[len(recs)-1]; last.Task != nil || last.Kind != KindUsage {
		t.Errorf("overhead record = %+v", last)
	}
}

func TestUnionMergedActivityGivesSameTotals(t *testing.T) {
	_, p := testBoard(t)
	tk, _ := p.Create(&Task{Title: "merge"}, roy, nil)
	p.Note(tk.ID, agentB, "a", &Usage{Tokens: Tokens{Total: 100}, Time: time.Minute})
	p.Note(tk.ID, agentB, "b", &Usage{Tokens: Tokens{Total: 200}})
	before, _ := p.Spent(tk.ID)
	recsBefore, _, _ := p.Activity()

	// A union merge of two copies keeps every line from both sides: here, all lines twice, in another order,
	// plus a broken line.
	path := p.activityPath()
	lines := strings.Split(strings.TrimSpace(readFile(t, path)), "\n")
	var merged []string
	for i := len(lines) - 1; i >= 0; i-- {
		merged = append(merged, lines[i])
	}
	merged = append(merged, lines...)
	merged = append(merged, "<<<<<<< not json")
	os.WriteFile(path, []byte(strings.Join(merged, "\n")+"\n"), 0o644)

	after, _ := p.Spent(tk.ID)
	if after.Tokens != before.Tokens || after.Time != before.Time || after.Records != before.Records {
		t.Errorf("before %+v after %+v", before, after)
	}
	recs, bad, _ := p.Activity()
	if len(recs) != len(recsBefore) || bad != 1 {
		t.Errorf("records %d (want %d), bad %d", len(recs), len(recsBefore), bad)
	}
	for i := range recs {
		if recs[i].ID != recsBefore[i].ID {
			t.Errorf("order differs at %d", i)
		}
	}
}

func TestActivityRecordsForEveryChange(t *testing.T) {
	_, p := testBoard(t)
	tk, _ := p.Create(&Task{Title: "track"}, roy, nil)
	p.Update(tk.ID, roy, Edit{Priority: ptr(High), Body: ptr("new body")}, "", nil)
	p.Claim(tk.ID, agentA)
	p.Note(tk.ID, agentA, strings.Repeat("x", 300), nil)
	recs, _, _ := p.Activity()
	var kinds []string
	for _, r := range recs {
		k := r.Kind
		if r.Kind == KindStatus {
			k += ":" + r.From + ">" + r.To
		}
		if r.Kind == KindEdit {
			k += ":" + strings.Join(r.Fields, ",")
		}
		kinds = append(kinds, k)
	}
	want := "status:>todo edit:priority,body edit:claim status:todo>doing note note"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("kinds = %s\nwant    %s", got, want)
	}
	if n := len([]rune(recs[len(recs)-1].Text)); n != noteTextMax {
		t.Errorf("note text length = %d", n)
	}
	if recs[3].Session != "claude-code:7f3a" || recs[3].Model != "claude-opus-5-5" || recs[3].Actor != "agent" {
		t.Errorf("status record identity = %+v", recs[3])
	}
}

func TestBoardGitattributes(t *testing.T) {
	b, _ := testBoard(t)
	if s := readFile(t, filepath.Join(b.Root(), ".gitattributes")); !strings.Contains(s, "activity.jsonl merge=union") {
		t.Errorf(".gitattributes = %q", s)
	}
}

func TestCreateProjectValidation(t *testing.T) {
	b, _ := testBoard(t)
	for _, c := range [][2]string{{"Bad Slug", "OK"}, {"ok", "lower"}, {"rpg", "NEW"}, {"other", "RPG"}} {
		if _, err := b.CreateProject(c[0], c[1], ""); err == nil {
			t.Errorf("CreateProject(%q, %q) succeeded", c[0], c[1])
		}
	}
	ps, err := b.Projects()
	if err != nil || len(ps) != 1 || ps[0].Key() != "RPG" {
		t.Errorf("projects = %v %v", ps, err)
	}
}

func TestSpentUsesSessionWideMeasuredSet(t *testing.T) {
	_, p := testBoard(t)
	tk, _ := p.Create(&Task{Title: "two claims"}, roy, nil)
	// The session's measured usage could not be tied to one task (task: null) ...
	p.RecordUsage("", agentA, &Usage{Tokens: Tokens{Input: 500}, Source: SourceMeasured})
	// ... so its self-report on the task must not be counted either: the measured record already has it.
	p.Note(tk.ID, agentA, "self", &Usage{Tokens: Tokens{Total: 9000}, Time: time.Hour})
	p.Note(tk.ID, agentB, "other session", &Usage{Tokens: Tokens{Total: 100}})

	s, err := p.Spent(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Tokens.Sum() != 100 || s.Time != 0 || s.Records != 1 {
		t.Errorf("task spent = %+v", s)
	}
	recs, _, _ := p.Activity()
	if all := SumUsage(recs); all.Tokens.Sum() != 600 {
		t.Errorf("project spent = %+v", all.Tokens)
	}
}

func TestSpentFollowsMoves(t *testing.T) {
	b, rpg := testBoard(t)
	art, _ := b.CreateProject("art", "ART", "Art")
	tk, _ := rpg.Create(&Task{Title: "wander"}, roy, &Usage{Time: 10 * time.Minute})
	rpg.Claim(tk.ID, agentA)
	moved, err := b.Move(rpg, tk.ID, art, roy)
	if err != nil {
		t.Fatal(err)
	}
	art.Note(moved.ID, agentB, "art work", &Usage{Tokens: Tokens{Total: 50}})
	back, err := b.Move(art, moved.ID, rpg, roy)
	if err != nil {
		t.Fatal(err)
	}
	rpg.Note(back.ID, agentB, "home again", &Usage{Time: 5 * time.Minute})

	s, err := rpg.Spent(back.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Time != 15*time.Minute || s.Tokens.Sum() != 50 || s.Records != 3 {
		t.Errorf("spent after two moves = %+v", s)
	}
	// The move out of doing is a status change in the new project.
	recs, _, _ := art.Activity()
	var status []string
	for _, r := range recs {
		if r.Kind == KindStatus && r.TaskID() == moved.ID {
			status = append(status, r.From+">"+r.To)
		}
	}
	if strings.Join(status, " ") != "doing>todo" {
		t.Errorf("status records in art = %v", status)
	}
	if got, _ := art.Get(moved.ID); got != nil {
		t.Error("moved-away task still in art")
	}
}

func TestBoardConfigDefaultsAndTimezone(t *testing.T) {
	root := t.TempDir()
	b, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if s := readFile(t, filepath.Join(root, "board.toml")); !strings.Contains(s, "[defaults]") {
		t.Errorf("board.toml template = %q", s)
	}
	if c := b.Config(); c.Timezone != "UTC" || c.Defaults != builtinDefaults {
		t.Errorf("config = %+v", c)
	}

	root = t.TempDir()
	os.WriteFile(filepath.Join(root, "board.toml"), []byte("port = 7777\ntimezone = \"Asia/Tokyo\"\n[defaults]\nstatus = \"backlog\"\ndoer = \"agent\"\n"), 0o644)
	b, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return time.Date(2026, 11, 17, 9, 1, 0, 0, time.UTC) }
	p, _ := b.CreateProject("rpg", "RPG", "")
	tk, err := p.Create(&Task{Title: "defaults"}, roy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != Backlog || tk.Doer != Agent || tk.Priority != Normal {
		t.Errorf("defaults: %s %s %s", tk.Status, tk.Doer, tk.Priority)
	}
	file := readFile(t, p.taskPath(tk.ID, false))
	if !strings.Contains(file, "- 2026-11-17 18:01 roy: created") || !strings.Contains(file, "created: 2026-11-17T09:01:00Z") {
		t.Errorf("times in file:\n%s", file)
	}
	got, _ := p.Get(tk.ID)
	if !got.Log[0].Time.Equal(tk.Created) {
		t.Errorf("log time read back = %s, want %s", got.Log[0].Time, tk.Created)
	}

	for _, bad := range []string{`timezone = "Mars/Olympus"`, "[defaults]\nstatus = \"later\"", "[defaults]\ndoer = \"robot\"", "[defaults]\npriority = \"meh\""} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "board.toml"), []byte(bad), 0o644)
		if _, err := Open(root); err == nil {
			t.Errorf("Open accepted board.toml %q", bad)
		}
	}
}

// A custom key that holds a list in the file is replaced when it is set as a field, never written twice.
func TestCustomFieldOverListValue(t *testing.T) {
	_, p := testBoard(t)
	p.Create(&Task{Title: "Crash"}, roy, nil)
	path := p.taskPath("RPG-1", false)
	data := strings.Replace(readFile(t, path), "---\n\n", "platform: [PS5, Win64]\nBad Key: kept\n---\n\n", 1)
	os.WriteFile(path, []byte(data), 0o644)
	if _, err := p.Update("RPG-1", roy, Edit{Custom: map[string]string{"platform": "PS5"}}, "", nil); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Count(got, "platform:") != 1 || !strings.Contains(got, "platform: PS5\n") || !strings.Contains(got, "Bad Key: kept\n") {
		t.Errorf("file:\n%s", got)
	}
	if _, err := p.Create(&Task{Title: "x", Custom: map[string]string{"Bad Key": "y"}}, roy, nil); err == nil {
		t.Error("a malformed custom key was accepted")
	}
}

// List edits add and remove single entries, so they keep what someone else added meanwhile.
func TestListOps(t *testing.T) {
	_, p := testBoard(t)
	p.Create(&Task{Title: "Rig", Labels: []string{"anim"}, Assignees: []string{"Mike"}}, roy, nil)
	agentLabels := []string{"anim", "blocked"}
	p.Update("RPG-1", roy, Edit{Labels: &agentLabels}, "", nil) // someone else adds "blocked"
	// A person working from the old copy adds "rig" and removes "anim".
	tk, err := p.Update("RPG-1", roy, Edit{AddLabels: []string{"rig", "BLOCKED"}, RemoveLabels: []string{"Anim"}, AddAssignees: []string{"jo"}, RemoveAssignees: []string{"mike"}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tk.Labels, ",") != "blocked,rig" || strings.Join(tk.Assignees, ",") != "jo" {
		t.Errorf("labels %v, assignees %v", tk.Labels, tk.Assignees)
	}
}

func TestLegacyAssigneeList(t *testing.T) {
	_, p := testBoard(t)
	p.Create(&Task{Title: "Rig"}, roy, nil)
	path := p.taskPath("RPG-1", false)
	os.WriteFile(path, []byte(strings.Replace(readFile(t, path), "assignees: []\n", "assignee: [Mike, mike, Jo]\n", 1)), 0o644)
	if tk, err := p.Get("RPG-1"); err != nil || strings.Join(tk.Assignees, ",") != "Mike,Jo" {
		t.Fatalf("read = %v %v", tk, err)
	}
}

// Auto-archive counts a hand edit (activity only) as a change, and stamps the archive deterministically.
func TestArchiveDoneFor(t *testing.T) {
	b, p := testBoard(t)
	p.Create(&Task{Title: "Door", Status: Done}, roy, nil)
	created := b.Now().UTC()
	day := 24 * time.Hour
	// A hand edit 5 days after creation left only an activity record.
	b.Now = func() time.Time { return created.Add(5 * day) }
	if err := p.RecordHandEdit("RPG-1", HandEdit{From: Review, To: Done}); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return created.Add(10 * day) }
	if ok, err := p.ArchiveDoneFor("RPG-1", roy, 7*day); ok || err != nil {
		t.Fatalf("archived 5 days after the hand edit: %v %v", ok, err)
	}
	b.Now = func() time.Time { return created.Add(13 * day) }
	ok, err := p.ArchiveDoneFor("RPG-1", roy, 7*day)
	if !ok || err != nil {
		t.Fatalf("not archived: %v %v", ok, err)
	}
	tk, _ := p.Get("RPG-1")
	last := tk.Log[len(tk.Log)-1]
	if !tk.Archived || !last.Time.Equal(created.Add(12*day).Truncate(time.Minute)) {
		t.Errorf("archived=%v, stamp %v, want %v", tk.Archived, last.Time, created.Add(12*day))
	}
}
