package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

const repoDir = "C:/work/rpg"

var me = board.Actor{Kind: "person", Name: "me"}

type fixture struct {
	t   *testing.T
	b   *board.Board
	p   *board.Project
	ix  *index.Index
	svc *Service
}

// newFixture builds a board with project RPG linked to git:github.com/acme/rpg, an index and a service
// whose detector maps repoDir to that remote.
func newFixture(t *testing.T) *fixture {
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
	if err := p.LinkRepo("git:github.com/acme/rpg"); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(filepath.Join(root, ".cache", "index.sqlite"), b, index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	det := vcs.NewDetectorWith(func(dir string) [][2]string {
		if strings.HasPrefix(filepath.ToSlash(dir), repoDir) {
			return [][2]string{{"origin", "git@github.com:acme/rpg.git"}}
		}
		return nil
	})
	return &fixture{t: t, b: b, p: p, ix: ix, svc: New(b, ix, det)}
}

func (f *fixture) task(tk *board.Task) string {
	f.t.Helper()
	got, err := f.p.Create(tk, me, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return got.ID
}

func (f *fixture) rebuild() {
	f.t.Helper()
	if err := f.ix.Rebuild(); err != nil {
		f.t.Fatal(err)
	}
}

func agentCaller(id string) Caller {
	return Caller{ID: "claude-code:" + id, Client: "claude-code/2.4.1", Cwd: repoDir + "/Source"}
}

func must[T any](t *testing.T) func(string, T, error) (string, T) {
	return func(text string, out T, err error) (string, T) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return text, out
	}
}

func TestClaimOrderAndProjectFromFolder(t *testing.T) {
	f := newFixture(t)
	f.task(&board.Task{Title: "low prio", Doer: board.Agent, Priority: board.Low})
	f.task(&board.Task{Title: "person work", Doer: board.Person, Priority: board.Urgent})
	urgent := f.task(&board.Task{Title: "urgent bug", Doer: board.Agent, Priority: board.Urgent})
	f.task(&board.Task{Title: "blocked urgent", Doer: board.Agent, Priority: board.Urgent, BlockedBy: []string{"RPG-1"}})
	f.rebuild()
	a := agentCaller("aaaaaa")
	text, out := must[ClaimOut](t)(f.svc.Claim(a, ClaimIn{}))
	if out.Claimed == nil || out.Claimed.ID != urgent || !strings.HasPrefix(text, "Claimed "+urgent+" doing (agent, urgent)") {
		t.Fatalf("claim = %q %+v", text, out.Claimed)
	}
	text, out = must[ClaimOut](t)(f.svc.Claim(a, ClaimIn{}))
	if out.Claimed == nil || out.Claimed.Title != "low prio" {
		t.Errorf("second claim = %q", text)
	}
	text, out = must[ClaimOut](t)(f.svc.Claim(a, ClaimIn{}))
	if out.Claimed != nil || !strings.Contains(text, "No unblocked todo agent task") {
		t.Errorf("third claim = %q", text)
	}
	// Outside a linked repo, project must be passed.
	if _, _, err := f.svc.Claim(Caller{ID: "x:1", Cwd: "D:/elsewhere"}, ClaimIn{}); err == nil || !strings.Contains(err.Error(), "pass project") {
		t.Errorf("no-repo err = %v", err)
	}
}

func TestConcurrentClaimsGetDifferentTasks(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 6; i++ {
		f.task(&board.Task{Title: fmt.Sprint("job ", i), Doer: board.Agent})
	}
	f.rebuild()
	var wg sync.WaitGroup
	got := make([]string, 6)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out, err := f.svc.Claim(agentCaller(fmt.Sprintf("a%05d", i)), ClaimIn{})
			if err != nil || out.Claimed == nil {
				t.Errorf("claim %d: %v %+v", i, err, out)
				return
			}
			got[i] = out.Claimed.ID
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range got {
		if id != "" && seen[id] {
			t.Errorf("%s claimed twice: %v", id, got)
		}
		seen[id] = true
	}
}

func TestClaimNoteReviewWithoutProject(t *testing.T) {
	f := newFixture(t)
	f.task(&board.Task{Title: "bridge", Doer: board.Agent})
	f.rebuild()
	a := agentCaller("aaaaaa")
	_, c := must[ClaimOut](t)(f.svc.Claim(a, ClaimIn{}))
	id := c.Claimed.ID
	text, _ := must[TaskReply](t)(f.svc.Note(a, NoteIn{ID: id, Text: "found it", UsageArgs: UsageArgs{Model: "claude-opus-5-5", SpentTokens: 42000, SpentTime: "25m"}}))
	if !strings.Contains(text, "spent ~42k tok, 25m") || strings.Contains(text, "Tip:") {
		t.Errorf("note reply = %q", text)
	}
	review := "review"
	text, out := must[TaskReply](t)(f.svc.Edit(a, EditIn{ID: id, Status: &review, Note: "done; resume with task_show"}))
	if out.Task.Status != "review" || out.Task.ClaimBy != "" {
		t.Errorf("edit = %q %+v", text, out.Task)
	}
	if !strings.Contains(text, "Tip: pass model / spent_tokens / spent_time.") {
		t.Errorf("missing tip: %q", text)
	}
	tk, _ := f.p.Get(id)
	if tk.Log[len(tk.Log)-1].Text != "done; resume with task_show" {
		t.Errorf("log = %+v", tk.Log)
	}
	recs, _, _ := f.p.Activity()
	var model string
	for _, r := range recs {
		if r.Kind == board.KindUsage {
			model = r.Model
		}
	}
	if model != "claude-opus-5-5" {
		t.Errorf("usage model = %q", model)
	}
}

func TestAgentRules(t *testing.T) {
	f := newFixture(t)
	person := f.task(&board.Task{Title: "person task", Doer: board.Person})
	done := f.task(&board.Task{Title: "finished", Doer: board.Agent, Status: board.Done})
	other := f.task(&board.Task{Title: "someone else's", Doer: board.Agent})
	f.rebuild()
	a, b := agentCaller("aaaaaa"), agentCaller("bbbbbb")
	title := "x"
	if _, _, err := f.svc.Edit(a, EditIn{ID: person, Title: &title}); err == nil {
		t.Error("edited a person task")
	}
	if _, _, err := f.svc.Note(a, NoteIn{ID: person, Text: "a note is fine"}); err != nil {
		t.Errorf("note on person task: %v", err)
	}
	if _, _, err := f.svc.Note(a, NoteIn{ID: done, Text: "no"}); err == nil {
		t.Error("noted a done task")
	}
	doing := "doing"
	if _, _, err := f.svc.Edit(a, EditIn{ID: other, Status: &doing}); err == nil {
		t.Error("set doing without a claim")
	}
	f.svc.Claim(b, ClaimIn{})
	if _, _, err := f.svc.Release(a, ReleaseIn{ID: other}); err == nil || !strings.Contains(err.Error(), "not stale") {
		t.Errorf("released another session's fresh claim: %v", err)
	}
	if _, _, err := f.svc.Release(b, ReleaseIn{ID: other}); err != nil {
		t.Errorf("release own claim: %v", err)
	}
}

func TestMeasuredSessionIgnoresSelfReports(t *testing.T) {
	f := newFixture(t)
	f.task(&board.Task{Title: "t", Doer: board.Agent})
	f.rebuild()
	a := agentCaller("aaaaaa")
	a.ClientSession = "sess-1"
	_, c := must[ClaimOut](t)(f.svc.Claim(a, ClaimIn{}))
	tr := writeTranscript(t, []string{
		promptLine("2026-11-18T10:00:00Z"),
		assistantLine("msg_1", "claude-opus-5-5", "2026-11-18T10:00:30Z", 10, 500, 2000, 100),
		assistantLine("msg_1", "claude-opus-5-5", "2026-11-18T10:00:31Z", 10, 500, 2000, 100), // same message again
		toolResultLine("2026-11-18T10:01:00Z"),
		assistantLine("msg_2", "claude-opus-5-5", "2026-11-18T10:05:00Z", 5, 300, 2600, 0),
	})
	res, err := f.svc.IngestTranscript(HookRequest{Client: "claude-code", ClientSession: "sess-1", TranscriptPath: tr, Cwd: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task != c.Claimed.ID || res.Tokens != 10+500+2000+100+5+300+2600 || res.Seconds != 300 {
		t.Errorf("hook result = %+v", res)
	}
	// Called again with no new lines: nothing more is counted.
	res, _ = f.svc.IngestTranscript(HookRequest{Client: "claude-code", ClientSession: "sess-1", TranscriptPath: tr, Cwd: repoDir})
	if res.Tokens != 0 {
		t.Errorf("second call counted %d tokens", res.Tokens)
	}
	text, _ := must[TaskReply](t)(f.svc.Note(a, NoteIn{ID: c.Claimed.ID, Text: "x", UsageArgs: UsageArgs{SpentTokens: 999999}}))
	if !strings.Contains(text, "measured") {
		t.Errorf("no measured note: %q", text)
	}
	sp, _ := f.p.Spent(c.Claimed.ID)
	if sp.Tokens.Sum() != 5515 || sp.Tokens.CacheRead != 4600 || sp.Self != 0 || sp.Time != 5*time.Minute {
		t.Errorf("spent = %+v", sp)
	}
}

func TestPartialTranscriptLineWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	full := assistantLine("msg_1", "m", "2026-11-18T10:00:30Z", 1, 2, 3, 4) + "\n"
	os.WriteFile(path, []byte(full[:len(full)/2]), 0o644)
	es, cur, err := scanTranscript(path, fileCursor{})
	if err != nil || cur.Offset != 0 || len(es) != 0 {
		t.Fatalf("partial: %+v %+v %v", es, cur, err)
	}
	os.WriteFile(path, []byte(full), 0o644)
	es, cur, _ = scanTranscript(path, cur)
	var tu transcriptUsage
	tu.add(es)
	if cur.Offset != int64(len(full)) || tu.byModel["m"].Sum() != 10 {
		t.Errorf("complete: %+v %+v", tu, cur)
	}
}

func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func promptLine(ts string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"do the task"}}`, ts)
}

func toolResultLine(ts string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`, ts)
}

func assistantLine(id, model, ts string, in, out, cacheRead, cacheCreate int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		ts, id, model, in, out, cacheRead, cacheCreate)
}

func TestBlockedByIsCheckedAndFollowsMoves(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.CreateProject("art", "ART", "Art"); err != nil {
		t.Fatal(err)
	}
	one := f.task(&board.Task{Title: "one", Doer: board.Agent})
	two := f.task(&board.Task{Title: "two", Doer: board.Agent, BlockedBy: []string{one}})
	f.rebuild()
	a := agentCaller("aaaaaa")

	for name, c := range map[string]struct {
		err  func() error
		want string
	}{
		"new missing": {func() error { _, _, err := f.svc.New(a, NewIn{Title: "x", BlockedBy: []string{"RPG-99"}}); return err }, "no task RPG-99"},
		"edit self":   {func() error { _, _, err := f.svc.Edit(a, EditIn{ID: one, BlockedBy: &[]string{one}}); return err }, "blocked by itself"},
		"edit cycle":  {func() error { _, _, err := f.svc.Edit(a, EditIn{ID: one, BlockedBy: &[]string{two}}); return err }, "RPG-1 → RPG-2 → RPG-1"},
	} {
		if err := c.err(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if got, _ := f.p.Get(one); len(got.BlockedBy) != 0 {
		t.Errorf("rejected edit was written: %v", got.BlockedBy)
	}

	text, out := must[TaskReply](t)(f.svc.Move(a, MoveIn{ID: one, To: "ART"}))
	if !strings.Contains(text, "blocked_by updated to "+out.Task.ID+" in "+two) {
		t.Errorf("move reply = %q", text)
	}
	if got, _ := f.p.Get(two); strings.Join(got.BlockedBy, ",") != out.Task.ID {
		t.Errorf("blocked_by after move = %v", got.BlockedBy)
	}
	_, found := must[TasksOut](t)(f.svc.Find(a, FindIn{Filter: "blocked:yes"}))
	if len(found.Tasks) != 1 || found.Tasks[0].ID != two {
		t.Errorf("blocked:yes after move = %+v", found.Tasks)
	}
}

func TestShowLogKeepsUsageSuffix(t *testing.T) {
	f := newFixture(t)
	id := f.task(&board.Task{Title: "t", Doer: board.Agent})
	f.p.Note(id, me, "worked", &board.Usage{Tokens: board.Tokens{Total: 42_000}, Time: 25 * time.Minute})
	f.rebuild()
	text, d := must[TaskDetail](t)(f.svc.Show(agentCaller("aaaaaa"), ShowIn{ID: id}))
	if last := d.Log[len(d.Log)-1]; !strings.HasSuffix(last, "me (+42k tok, +25m): worked") || !strings.Contains(text, last) {
		t.Errorf("log = %q", d.Log)
	}
}
