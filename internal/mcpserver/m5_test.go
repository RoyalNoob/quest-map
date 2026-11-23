package mcpserver

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
)

func TestAssignee(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a := agentCaller("aaaaaa")
	text, out := must[TaskReply](t)(f.svc.New(a, NewIn{Title: "Rig the boss", Doer: "agent", Assignees: []string{" Mika ", "Jo", "mika"}}))
	if strings.Join(out.Task.Assignees, ",") != "Mika,Jo" || !strings.Contains(text, "@Mika, @Jo") {
		t.Fatalf("new = %q %+v", text, out.Task)
	}
	must[TaskReply](t)(f.svc.New(a, NewIn{Title: "Nobody's yet", Doer: "person"}))
	f.rebuild()
	_, found := must[TasksOut](t)(f.svc.Find(a, FindIn{Filter: "assignee:mika"}))
	if len(found.Tasks) != 1 || found.Tasks[0].ID != out.Task.ID || strings.Join(found.Tasks[0].Assignees, ",") != "Mika,Jo" {
		t.Errorf("assignee:mika = %+v", found.Tasks)
	}
	_, found = must[TasksOut](t)(f.svc.Find(a, FindIn{Filter: "-assignee:mika"}))
	if len(found.Tasks) != 1 || found.Tasks[0].Title != "Nobody's yet" {
		t.Errorf("-assignee:mika = %+v", found.Tasks)
	}
	_, found = must[TasksOut](t)(f.svc.Find(a, FindIn{Filter: "assignee:jo"}))
	if len(found.Tasks) != 1 {
		t.Errorf("assignee:jo = %+v", found.Tasks)
	}
	none := []string{}
	must[TaskReply](t)(f.svc.Edit(a, EditIn{ID: out.Task.ID, Assignees: &none}))
	if tk, _ := f.p.Get(out.Task.ID); len(tk.Assignees) != 0 {
		t.Errorf("cleared assignees = %q", tk.Assignees)
	}
}

func TestNewFromTemplate(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a := agentCaller("aaaaaa")
	// Fields the call sets win; the template fills the rest and adds its labels.
	_, out := must[TaskReply](t)(f.svc.New(a, NewIn{Title: "Bridge mesh", Template: "prop asset", Priority: "high", Labels: []string{"bridge"}}))
	tk, _ := f.p.Get(out.Task.ID)
	if tk.Discipline != "art" || tk.Priority != "high" || tk.Pipeline == nil || tk.Pipeline.Name != "prop" ||
		strings.Join(tk.Labels, ",") != "bridge,asset" || !strings.Contains(tk.Body, "## Budget") {
		t.Errorf("task = %+v", tk)
	}
	_, out = must[TaskReply](t)(f.svc.New(a, NewIn{Title: "Door crash", Template: "Crash", Body: "Crashes when the door opens."}))
	tk, _ = f.p.Get(out.Task.ID)
	if tk.Body != "Crashes when the door opens." || tk.Priority != "high" || strings.Join(tk.Labels, ",") != "bug,crash" {
		t.Errorf("crash task = %+v", tk)
	}
	if _, _, err := f.svc.New(a, NewIn{Title: "x", Template: "Vehicle"}); err == nil || !strings.Contains(err.Error(), "templates: Bug, Crash") {
		t.Errorf("unknown template err = %v", err)
	}
}

func TestPipelineAdvanceGameDev(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a := agentCaller("aaaaaa")
	text, out := must[TaskReply](t)(f.svc.New(a, NewIn{Title: "Bridge mesh", Doer: "agent", Discipline: "art", Asset: "/Game/Props/SM_Bridge_Mid", Pipeline: "prop"}))
	if out.Task.ID == "" || strings.Contains(text, "Note:") {
		t.Fatalf("new = %q", text)
	}
	id := out.Task.ID
	tk, _ := f.p.Get(id)
	if tk.Pipeline == nil || tk.Pipeline.Name != "prop" || tk.Pipeline.At != "blockout" {
		t.Fatalf("pipeline = %+v", tk.Pipeline)
	}
	f.rebuild()
	if _, c := must[ClaimOut](t)(f.svc.Claim(a, ClaimIn{Discipline: "art"})); c.Claimed == nil || c.Claimed.ID != id {
		t.Fatalf("claim discipline:art = %+v", c.Claimed)
	}
	text, _ = must[TaskReply](t)(f.svc.Advance(a, AdvanceIn{ID: id, Note: "blockout approved"}))
	if !strings.HasPrefix(text, id+" prop: blockout → model (step 2 of 6)") {
		t.Errorf("advance = %q", text)
	}
	tk, _ = f.p.Get(id)
	if tk.Pipeline.At != "model" || !strings.Contains(tk.Log[len(tk.Log)-1].Text, "asset prop: blockout → model. blockout approved") {
		t.Errorf("after advance: %+v / %q", tk.Pipeline, tk.Log[len(tk.Log)-1].Text)
	}
	must[TaskReply](t)(f.svc.Advance(a, AdvanceIn{ID: id, To: "in-engine"}))
	if _, _, err := f.svc.Advance(a, AdvanceIn{ID: id}); err == nil || !strings.Contains(err.Error(), "last step") {
		t.Errorf("past the end: %v", err)
	}
	if _, _, err := f.svc.Advance(a, AdvanceIn{ID: id, To: "rig"}); err == nil || !strings.Contains(err.Error(), "no step") {
		t.Errorf("unknown step: %v", err)
	}
	if _, _, err := f.svc.New(a, NewIn{Title: "x", Pipeline: "spaceship"}); err == nil || !strings.Contains(err.Error(), "prop, character, level, vfx") {
		t.Errorf("unknown pipeline: %v", err)
	}
}

func TestReleasesAndSuiteNotes(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a := agentCaller("aaaaaa")
	text, out := must[ReleaseOut](t)(f.svc.RecordRelease(a, RecordReleaseIn{ID: "b2026-11-19.1", Target: "Win64", Revision: "abc123"}))
	if text != "Recorded build b2026-11-19.1 (platform Win64) in rpg." || out.Release.Date.IsZero() {
		t.Errorf("record = %q %+v", text, out)
	}
	if _, _, err := f.svc.RecordRelease(a, RecordReleaseIn{ID: "b2026-11-19.1"}); err == nil {
		t.Error("duplicate id recorded")
	}
	text, _ = must[ReleaseOut](t)(f.svc.RecordRelease(a, RecordReleaseIn{ID: "b2026-11-19.2", Target: "Switch2"}))
	if !strings.Contains(text, "not in the usual platforms") {
		t.Errorf("unusual platform: %q", text)
	}
	text, _ = must[TaskReply](t)(f.svc.New(a, NewIn{Title: "Shader stutter", Discipline: "shaders", Milestone: "alpha"}))
	if !strings.Contains(text, `discipline "shaders" is not in this project's list`) || !strings.Contains(text, `milestone "alpha" is not defined`) {
		t.Errorf("notes = %q", text)
	}
	// found:latest uses the newest release.
	f.task(&board.Task{Title: "Crash on load", FoundIn: "b2026-11-19.2"})
	f.task(&board.Task{Title: "Old crash", FoundIn: "b2026-11-19.1"})
	f.rebuild()
	rows, err := f.ix.FindString("found:latest", index_FindAll())
	if err != nil || len(rows) != 1 || rows[0].Title != "Crash on load" {
		t.Errorf("found:latest = %+v %v", rows, err)
	}
}

func TestFeedbackReportGameDev(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a := agentCaller("aaaaaa")
	png := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...))
	text, out := must[TaskReply](t)(f.svc.Feedback(a, FeedbackIn{Text: "Fell through the bridge\nNear the middle span, while sprinting.",
		Release: "b2026-11-19.1", Reporter: "tester-7", ScreenshotB64: png, ScreenshotName: "fall.png"}))
	if !strings.HasPrefix(text, "Filed playtest note as RPG-1 backlog (person, qa)") || !strings.Contains(text, "found in build b2026-11-19.1") {
		t.Errorf("reply = %q", text)
	}
	tk, _ := f.p.Get(out.Task.ID)
	if tk.Status != board.Backlog || tk.Discipline != "qa" || tk.FoundIn != "b2026-11-19.1" || strings.Join(tk.Labels, ",") != "playtest" ||
		tk.Title != "Fell through the bridge" || !strings.Contains(tk.Body, "Playtest note from tester-7 via agent claude-code:aaaaaa") ||
		!strings.Contains(tk.Body, "![fall.png](../attachments/RPG-1/fall.png)") {
		t.Errorf("task = %+v\n%s", tk, tk.Body)
	}
	if _, err := os.Stat(filepath.Join(f.p.Dir(), "attachments", "RPG-1", "fall.png")); err != nil {
		t.Errorf("screenshot: %v", err)
	}
}

// TestOtherSuiteWithoutCodeChanges is the second half of the M5 acceptance check: a suite file alone changes
// wording, disciplines, pipelines and the asset resolver.
func TestOtherSuiteWithoutCodeChanges(t *testing.T) {
	f := newFixture(t)
	os.MkdirAll(filepath.Join(f.b.Root(), "suites"), 0o755)
	os.WriteFile(filepath.Join(f.b.Root(), "suites", "webapp.toml"), []byte(`
title = "Web app"
disciplines = ["frontend", "backend", "qa"]
release_targets = ["staging", "prod"]

[words]
release = "deploy"
releases = "deploys"
target = "environment"
item = "feature"
feedback = "user report"

[[pipelines]]
name = "feature"
steps = ["spec", "build", "review", "ship"]

[intake]
status = "todo"
doer = "agent"
discipline = "frontend"
labels = ["from-users"]
`), 0o644)
	if err := f.p.SetSuite("webapp"); err != nil {
		t.Fatal(err)
	}
	f.rebuild()
	a := agentCaller("aaaaaa")
	st, problem := f.svc.suiteFor(f.p)
	if problem != "" || st.Name != "webapp" || st.Resolver().Name() != "path" {
		t.Fatalf("suite = %+v %q", st, problem)
	}
	text, out := must[TaskReply](t)(f.svc.New(a, NewIn{Title: "Checkout page", Doer: "agent", Discipline: "frontend", Asset: "web/src/Checkout.tsx", Pipeline: "feature"}))
	if strings.Contains(text, "Note:") {
		t.Errorf("new = %q", text)
	}
	text, _ = must[TaskReply](t)(f.svc.Advance(a, AdvanceIn{ID: out.Task.ID}))
	if !strings.HasPrefix(text, out.Task.ID+" feature: spec → build (step 2 of 4)") {
		t.Errorf("advance = %q", text)
	}
	tk, _ := f.p.Get(out.Task.ID)
	if !strings.Contains(tk.Log[len(tk.Log)-1].Text, "feature feature: spec → build") {
		t.Errorf("log = %q", tk.Log[len(tk.Log)-1].Text)
	}
	if _, _, err := f.svc.New(a, NewIn{Title: "x", Pipeline: "prop"}); err == nil || !strings.Contains(err.Error(), "pipelines: feature") {
		t.Errorf("game pipeline in web suite: %v", err)
	}
	text, _ = must[ReleaseOut](t)(f.svc.RecordRelease(a, RecordReleaseIn{ID: "v1.4.0", Target: "prod"}))
	if text != "Recorded deploy v1.4.0 (environment prod) in rpg." {
		t.Errorf("record = %q", text)
	}
	text, fb := must[TaskReply](t)(f.svc.Feedback(a, FeedbackIn{Text: "Pay button does nothing", Release: "v1.4.0"}))
	if !strings.HasPrefix(text, "Filed user report as") || !strings.Contains(text, "found in deploy v1.4.0") {
		t.Errorf("feedback = %q", text)
	}
	tk, _ = f.p.Get(fb.Task.ID)
	if tk.Status != board.Todo || tk.Doer != board.Agent || tk.Discipline != "frontend" || strings.Join(tk.Labels, ",") != "from-users" {
		t.Errorf("intake task = %+v", tk)
	}
	text, _ = must[TaskReply](t)(f.svc.New(a, NewIn{Title: "y", Discipline: "art"}))
	if !strings.Contains(text, `discipline "art" is not in this project's list (frontend, backend, qa)`) {
		t.Errorf("art in web suite: %q", text)
	}
}

func TestBrokenSuiteFileFallsBack(t *testing.T) {
	f := newFixture(t)
	os.MkdirAll(filepath.Join(f.b.Root(), "suites"), 0o755)
	os.WriteFile(filepath.Join(f.b.Root(), "suites", "gamedev-unreal.toml"), []byte("disciplines = ["), 0o644)
	f.rebuild()
	text, _ := must[TaskReply](t)(f.svc.New(agentCaller("aaaaaa"), NewIn{Title: "still works", Discipline: "art"}))
	if !strings.Contains(text, "suite problem") || !strings.Contains(text, "gamedev-unreal.toml") {
		t.Errorf("reply = %q", text)
	}
}

func index_FindAll() index.FindOptions { return index.FindOptions{} }

func TestFeedbackBodyAndUnknownRelease(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a := agentCaller("aaaaaa")
	must[ReleaseOut](t)(f.svc.RecordRelease(a, RecordReleaseIn{ID: "b2026-11-19.1", Target: "Win64"}))

	text, out := must[TaskReply](t)(f.svc.Feedback(a, FeedbackIn{Text: "Fell through the bridge\nNear the middle span.", Release: "B2026-11-19.1"}))
	tk, _ := f.p.Get(out.Task.ID)
	if strings.Contains(text, "Note:") || strings.Contains(tk.Body, "not recorded") {
		t.Errorf("recorded build (other case) got a note: %q\n%s", text, tk.Body)
	}
	if strings.HasPrefix(tk.Body, "Fell through") || !strings.HasPrefix(tk.Body, "Near the middle span.") {
		t.Errorf("title repeated in body:\n%s", tk.Body)
	}

	text, out = must[TaskReply](t)(f.svc.Feedback(a, FeedbackIn{Text: "Audio pops in menu", Release: "b1999-01-01.9"}))
	tk, _ = f.p.Get(out.Task.ID)
	if !strings.Contains(text, "Note: Build b1999-01-01.9 is not recorded in rpg") || tk.FoundIn != "b1999-01-01.9" ||
		!strings.Contains(tk.Body, "Build b1999-01-01.9 was not recorded when this was filed.") || strings.HasPrefix(tk.Body, "Audio pops") {
		t.Errorf("unknown build: %q\n%s", text, tk.Body)
	}

	long := strings.Repeat("word ", 30) + "end"
	_, out = must[TaskReply](t)(f.svc.Feedback(a, FeedbackIn{Text: long}))
	tk, _ = f.p.Get(out.Task.ID)
	if !strings.HasSuffix(tk.Title, "…") || !strings.HasPrefix(tk.Body, long) {
		t.Errorf("a shortened title keeps the full line in the body: %q\n%s", tk.Title, tk.Body)
	}
}

func TestSuiteProblemSaysWhatIsUsed(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.b.Root(), "suites")
	os.MkdirAll(dir, 0o755)
	file := filepath.Join(dir, "webapp.toml")
	os.WriteFile(file, []byte("[words]\nrelease = \"deploy\"\n[adapters]\nassets = \"path\"\n"), 0o644)
	if err := f.p.SetSuite("webapp"); err != nil {
		t.Fatal(err)
	}
	if st, problem := f.svc.suiteFor(f.p); problem != "" || st.Words.Release != "deploy" {
		t.Fatalf("good file: %q %+v", problem, st.Words)
	}
	// Broken after a good load: the last good version stays in use, and the note says so.
	os.WriteFile(file, []byte("[words]\nrelease = \"deploy\"\n[adapters]\nassets = \"s3\"\n"), 0o644)
	st, problem := f.svc.suiteFor(f.p)
	if st.Name != "webapp" || st.Words.Release != "deploy" || !strings.Contains(problem, `unknown asset resolver "s3"`) ||
		!strings.Contains(problem, "using the suite files as they last loaded") || strings.Contains(problem, "default suite") {
		t.Errorf("broken after good: %q %s", problem, st.Name)
	}
	// A project naming a suite that does not exist really gets the default.
	if err := f.p.SetSuite("nope"); err == nil {
		os.WriteFile(file, []byte("[words]\nrelease = \"deploy\"\n"), 0o644)
		st, problem = f.svc.suiteFor(f.p)
		if st.Name != "gamedev-unreal" || !strings.Contains(problem, "using the default suite gamedev-unreal") {
			t.Errorf("unknown suite: %q %s", problem, st.Name)
		}
	}
}

func TestBrokenSuiteFileOnFirstLoad(t *testing.T) {
	f := newFixture(t)
	os.MkdirAll(filepath.Join(f.b.Root(), "suites"), 0o755)
	os.WriteFile(filepath.Join(f.b.Root(), "suites", "gamedev-unreal.toml"), []byte("disciplines = ["), 0o644)
	st, problem := f.svc.suiteFor(f.p)
	if st == nil || st.Name != "gamedev-unreal" || !strings.Contains(problem, "using the built-in suites only") {
		t.Errorf("first load broken: %q", problem)
	}
}

func TestShowListsAssetFiles(t *testing.T) {
	f := newFixture(t)
	repo := t.TempDir()
	os.Mkdir(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(repo, "Bridge.uproject"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(repo, "Content", "Props"), 0o755)
	os.WriteFile(filepath.Join(repo, "Content", "Props", "SM_Bridge_Mid.uasset"), nil, 0o644)
	id := f.task(&board.Task{Title: "bridge", Asset: "/Game/Props/SM_Bridge_Mid"})
	f.rebuild()
	in := Caller{ID: "claude-code:aaaaaa", Cwd: filepath.Join(repo, "Content")}
	text, d := must[TaskDetail](t)(f.svc.Show(in, ShowIn{ID: id}))
	if strings.Join(d.Files, ",") != "Content/Props/SM_Bridge_Mid.uasset" || !strings.Contains(text, "files: Content/Props/SM_Bridge_Mid.uasset") {
		t.Errorf("unreal files = %v\n%s", d.Files, text)
	}
	// Outside a working copy there is nothing to map to.
	if _, d := must[TaskDetail](t)(f.svc.Show(Caller{ID: "claude-code:aaaaaa", Cwd: t.TempDir()}, ShowIn{ID: id})); len(d.Files) != 0 {
		t.Errorf("no repo: %v", d.Files)
	}
	// The same task under a suite with the path resolver: the value is the repo-relative path itself.
	os.MkdirAll(filepath.Join(f.b.Root(), "suites"), 0o755)
	os.WriteFile(filepath.Join(f.b.Root(), "suites", "webapp.toml"), []byte("[adapters]\nassets = \"path\"\n"), 0o644)
	f.p.SetSuite("webapp")
	web := f.task(&board.Task{Title: "page", Asset: `src\pages\Checkout.tsx`})
	if _, d := must[TaskDetail](t)(f.svc.Show(in, ShowIn{ID: web})); strings.Join(d.Files, ",") != "src/pages/Checkout.tsx" {
		t.Errorf("path files = %v", d.Files)
	}
}

func TestCustomFieldsMCP(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a := agentCaller("aaaaaa")
	text, out := must[TaskReply](t)(f.svc.New(a, NewIn{Title: "Door crash", Doer: "agent", Fields: map[string]string{"platform": "PS5", "ue_map": "/Game/Maps/L_Gate"}}))
	if out.Task.Fields["platform"] != "PS5" || !strings.Contains(text, `Platform "PS5" is not in this project's list`) {
		t.Fatalf("new = %q %+v", text, out.Task.Fields)
	}
	if _, _, err := f.svc.New(a, NewIn{Title: "x", Fields: map[string]string{"platfrom": "Win64"}}); err == nil || !strings.Contains(err.Error(), "fields: platform, ue_map") {
		t.Errorf("typo err = %v", err)
	}
	f.rebuild()
	_, found := must[TasksOut](t)(f.svc.Find(a, FindIn{Filter: "platform:ps5"}))
	if len(found.Tasks) != 1 || found.Tasks[0].Fields["ue_map"] != "/Game/Maps/L_Gate" {
		t.Errorf("platform:ps5 = %+v", found.Tasks)
	}
	must[TaskReply](t)(f.svc.Edit(a, EditIn{ID: out.Task.ID, Fields: map[string]string{"platform": "Win64", "ue_map": ""}}))
	tk, _ := f.p.Get(out.Task.ID)
	if tk.Custom["platform"] != "Win64" || tk.Custom["ue_map"] != "" {
		t.Errorf("after edit = %v", tk.Custom)
	}
	if _, _, err := f.svc.Find(a, FindIn{Filter: "colour:red"}); err == nil || !strings.Contains(err.Error(), "platform") {
		t.Errorf("unknown key err = %v", err)
	}
}
