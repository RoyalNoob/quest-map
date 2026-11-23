package mcpserver

import (
	"os"
	"strings"
	"testing"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// The M3 multi-agent test cases (spec §11 M3). Subagents of one client share one MCP session, so they share
// a Caller; updates are routed by task.

func ptr[T any](v T) *T { return &v }

// sharedSession sets up two subagents of one session, each having claimed its own task.
func sharedSession(t *testing.T) (f *fixture, s Caller, taskA, taskB string) {
	f = newFixture(t)
	f.task(&board.Task{Title: "subagent A's task", Doer: board.Agent, Priority: board.High})
	f.task(&board.Task{Title: "subagent B's task", Doer: board.Agent})
	f.rebuild()
	s = agentCaller("shared")
	_, a := must[ClaimOut](t)(f.svc.Claim(s, ClaimIn{}))
	_, b := must[ClaimOut](t)(f.svc.Claim(s, ClaimIn{}))
	return f, s, a.Claimed.ID, b.Claimed.ID
}

func note(t *testing.T, f *fixture, c Caller, id, text string) string {
	t.Helper()
	out, _ := must[TaskReply](t)(f.svc.Note(c, NoteIn{ID: id, Text: text, UsageArgs: UsageArgs{SpentTime: "1m"}}))
	return out
}

func TestSubagentsSharingASessionGetOnlyTheirTasksUpdates(t *testing.T) {
	f, s, a, b := sharedSession(t)
	f.p.Note(a, me, "use the LOD1 mesh", nil)
	if r := note(t, f, s, b, "B working"); strings.Contains(r, "Updates:") {
		t.Errorf("B got A's update: %q", r)
	}
	if r := note(t, f, s, a, "A working"); !strings.Contains(r, `person commented on `+a+`: "use the LOD1 mesh"`) {
		t.Errorf("A missed its update: %q", r)
	}
}

func TestPersonEditMidWorkWarnsOnlyThatSubagent(t *testing.T) {
	f, s, a, b := sharedSession(t)
	if _, err := f.p.Update(a, me, board.Edit{Body: ptr("New requirements: use Nanite.")}, "", nil); err != nil {
		t.Fatal(err)
	}
	r := note(t, f, s, a, "continuing")
	if !strings.HasPrefix(r, a+" changed by a person since your claim – re-read it (task_show).") || !strings.Contains(r, "changed "+a+": body") {
		t.Errorf("A reply = %q", r)
	}
	if r := note(t, f, s, b, "B continuing"); strings.Contains(r, "changed by a person") || strings.Contains(r, "Updates:") {
		t.Errorf("B reply = %q", r)
	}
}

func TestDecisionDoneReachesTheWaitingTask(t *testing.T) {
	f := newFixture(t)
	work := f.task(&board.Task{Title: "build the menu", Doer: board.Agent})
	f.rebuild()
	c := agentCaller("aaaaaa")
	must[ClaimOut](t)(f.svc.Claim(c, ClaimIn{}))
	decision := f.task(&board.Task{Title: "Decide: menu style", Doer: board.Person})
	f.p.Update(work, me, board.Edit{BlockedBy: &[]string{decision}}, "", nil)
	note(t, f, c, work, "waiting for the decision") // sees the blocked_by change, cursor moves on
	f.p.Update(decision, me, board.Edit{Status: ptr(board.Done)}, "go with the diegetic menu", nil)
	r := note(t, f, c, work, "checking")
	if !strings.Contains(r, decision+" ("+work+" waits on it) → done") || !strings.Contains(r, "go with the diegetic menu") {
		t.Errorf("reply = %q", r)
	}
}

func TestHandEditGivesTheSameUpdateAsABoardEdit(t *testing.T) {
	viaBoard := func() string {
		f, s, a, _ := sharedSession(t)
		f.p.Update(a, me, board.Edit{Priority: ptr(board.Urgent)}, "", nil)
		return note(t, f, s, a, "x")
	}()
	viaFile := func() string {
		f, s, a, _ := sharedSession(t)
		path := f.p.TaskPath(a, false)
		data, _ := os.ReadFile(path)
		os.WriteFile(path, []byte(strings.Replace(string(data), "priority: high", "priority: urgent", 1)), 0o644)
		if _, err := f.ix.RefreshFile(path); err != nil { // what the watcher does
			t.Fatal(err)
		}
		if err := f.ix.FlushHandEdits(); err != nil { // hand-edit records are written after a short wait
			t.Fatal(err)
		}
		return note(t, f, s, a, "x")
	}()
	for name, r := range map[string]string{"board": viaBoard, "file": viaFile} {
		if !strings.Contains(r, "changed by a person since your claim") || !strings.Contains(r, ": priority") {
			t.Errorf("%s: %q", name, r)
		}
	}
}

func TestDigestIsNotRepeatedNorLostToASibling(t *testing.T) {
	f, s, a, b := sharedSession(t)
	f.p.Note(a, me, "first remark", nil)
	note(t, f, s, b, "sibling calls first") // must not consume A's update
	if r := note(t, f, s, a, "A"); !strings.Contains(r, "first remark") {
		t.Errorf("A lost its update: %q", r)
	}
	if r := note(t, f, s, a, "A again"); strings.Contains(r, "Updates:") {
		t.Errorf("update repeated: %q", r)
	}
	// Reads move the cursor too.
	f.p.Note(a, me, "second remark", nil)
	text, _ := must[TaskDetail](t)(f.svc.Show(s, ShowIn{ID: a}))
	if !strings.Contains(text, "second remark") {
		t.Errorf("show: %q", text)
	}
	if r := note(t, f, s, a, "A third"); strings.Contains(r, "Updates:") {
		t.Errorf("after show: %q", r)
	}
}

func TestSessionWithTwoClaimsRecordsMeasuredUsageWithoutTask(t *testing.T) {
	f, s, _, _ := sharedSession(t)
	s.ClientSession = "sess-shared"
	f.svc.session(s) // the proxy's headers told the server this client session
	tr := writeTranscript(t, []string{promptLine("2026-11-18T10:00:00Z"), assistantLine("m1", "claude-opus-5-5", "2026-11-18T10:02:00Z", 1, 2, 3, 4)})
	res, err := f.svc.IngestTranscript(HookRequest{ClientSession: "sess-shared", TranscriptPath: tr, Cwd: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task != "" || res.Project != "rpg" {
		t.Errorf("result = %+v, want project overhead", res)
	}
	recs, _, _ := f.p.Activity()
	last := recs[len(recs)-1]
	if last.Kind != board.KindUsage || last.Task != nil || last.Source != board.SourceMeasured {
		t.Errorf("record = %+v", last)
	}
}

func TestSeparateProcessesInOneRepo(t *testing.T) {
	f := newFixture(t)
	f.task(&board.Task{Title: "one", Doer: board.Agent})
	f.task(&board.Task{Title: "two", Doer: board.Agent})
	f.rebuild()
	p1, p2 := agentCaller("111111"), agentCaller("222222")
	_, c1 := must[ClaimOut](t)(f.svc.Claim(p1, ClaimIn{}))
	_, c2 := must[ClaimOut](t)(f.svc.Claim(p2, ClaimIn{}))
	if c1.Claimed.ID == c2.Claimed.ID {
		t.Fatal("same task claimed twice")
	}
	// Each process's notes reach the other only through the tasks they touch.
	note(t, f, p1, c1.Claimed.ID, "p1 note")
	if r := note(t, f, p2, c2.Claimed.ID, "p2 note"); strings.Contains(r, "Updates:") {
		t.Errorf("p2 got p1's update: %q", r)
	}
	f.svc.Show(p2, ShowIn{ID: c1.Claimed.ID}) // p2 starts watching p1's task
	note(t, f, p1, c1.Claimed.ID, "p1 again")
	text, _ := must[TaskDetail](t)(f.svc.Show(p2, ShowIn{ID: c1.Claimed.ID}))
	if !strings.Contains(text, `claude-code:111111 commented on `+c1.Claimed.ID+`: "p1 again"`) {
		t.Errorf("p2 show: %q", text)
	}
}

func TestAnnouncements(t *testing.T) {
	f := newFixture(t)
	ann := f.task(&board.Task{Title: "Team announcements", Labels: []string{"announce"}})
	f.task(&board.Task{Title: "work", Doer: board.Agent})
	f.rebuild()
	c := agentCaller("aaaaaa")
	f.svc.Find(c, FindIn{})
	f.p.Note(ann, me, "Freeze on main until 5pm", nil)
	f.ix.Rebuild()
	text, _ := must[TasksOut](t)(f.svc.Find(c, FindIn{}))
	if !strings.Contains(text, `Announcements: `+ann+`: "Freeze on main until 5pm"`) {
		t.Errorf("find = %q", text)
	}
	text, _ = must[ClaimOut](t)(f.svc.Claim(c, ClaimIn{}))
	if strings.Contains(text, "Announcements:") {
		t.Errorf("announcement repeated: %q", text)
	}
}
