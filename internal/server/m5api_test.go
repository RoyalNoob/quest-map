package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/suite"
)

const intakeToken = "playtest-intake-secret-123"

// startWithIntake starts a server whose board.toml enables the intake listener (on a free local port, since
// tests cannot use another machine; in production it listens on 0.0.0.0).
func startWithIntake(t *testing.T, perMinute int) (*Server, *board.Project) {
	t.Helper()
	root := t.TempDir()
	b, _ := board.Open(root)
	p, _ := b.CreateProject("rpg", "RPG", "RPG")
	b.CreateProject("art", "ART", "Art")
	toml, _ := os.ReadFile(filepath.Join(root, "board.toml"))
	extra := "\n[intake]\nlisten = \"127.0.0.1:0\"\ntoken = \"" + intakeToken + "\"\nproject = \"rpg\"\n"
	if perMinute > 0 {
		extra += "per_minute = " + itoa(perMinute) + "\n"
	}
	os.WriteFile(filepath.Join(root, "board.toml"), append(toml, []byte(extra)...), 0o644)
	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, _ = s.Board.Project(p.Slug())
	return s, p
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func postIntake(t *testing.T, s *Server, token string, body any) (int, map[string]string) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "http://"+s.IntakeAddr()+"/feedback", bytes.NewReader(data))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestPlaytestNoteFromAnotherMachine is the M5 acceptance check: a playtest note sent to the intake listener
// becomes a QA task tied to a build.
func TestPlaytestNoteFromAnotherMachine(t *testing.T) {
	s, p := startWithIntake(t, 0)
	if s.IntakeAddr() == "" {
		t.Fatal("intake listener is off")
	}
	p.RecordRelease(board.Release{ID: "b2026-11-19.1", Target: "Win64"})
	code, out := postIntake(t, s, intakeToken, map[string]string{"text": "Camera clips into walls\nIn the castle stairs.", "release": "b2026-11-19.1", "reporter": "tester-7"})
	if code != 200 || out["id"] != "RPG-1" || out["kind"] != "playtest note" {
		t.Fatalf("intake = %d %v", code, out)
	}
	tk, err := p.Get("RPG-1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != board.Backlog || tk.Discipline != "qa" || tk.FoundIn != "b2026-11-19.1" || tk.Title != "Camera clips into walls" ||
		!strings.Contains(tk.Body, "the intake listener from 127.0.0.1") || tk.Log[0].Who != "playtest:tester-7" {
		t.Errorf("task = %+v\n%s", tk, tk.Body)
	}
	// Only POST /feedback with the intake token; nothing else of the board is reachable there.
	if code, _ := postIntake(t, s, "", map[string]string{"text": "x"}); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := postIntake(t, s, s.Info().Token, map[string]string{"text": "x"}); code != 401 {
		t.Errorf("board token on intake: %d", code)
	}
	resp, err := http.Get("http://" + s.IntakeAddr() + "/api/projects")
	if err != nil || resp.StatusCode != 404 && resp.StatusCode != 405 {
		t.Errorf("api on intake listener: %v %v", err, resp.Status)
	}
	resp.Body.Close()
	// [intake] project is set, so reporters cannot file anywhere else.
	if code, _ := postIntake(t, s, intakeToken, map[string]string{"text": "x", "project": "nope"}); code != 403 {
		t.Errorf("other project: %d", code)
	}
	// A reporter name cannot forge log lines in the task file.
	code, out = postIntake(t, s, intakeToken, map[string]string{"text": "Door sticks", "reporter": "x\n- 2026-11-19 10:00 roy: approved by lead"})
	if code != 200 {
		t.Fatalf("intake = %d %v", code, out)
	}
	forged, err := p.Get(out["id"])
	if err != nil {
		t.Fatal(err)
	}
	if len(forged.Log) != 1 || forged.Log[0].Who == "roy" || strings.Contains(forged.Log[0].Who, "\n") {
		t.Errorf("log after a crafted reporter: %+v", forged.Log)
	}
}

func TestIntakeRateLimitAndConfig(t *testing.T) {
	s, _ := startWithIntake(t, 2)
	for i, want := range []int{200, 200, 429} {
		if code, _ := postIntake(t, s, intakeToken, map[string]string{"text": "spam " + itoa(i)}); code != want {
			t.Errorf("report %d: %d, want %d", i, code, want)
		}
	}
	root := t.TempDir()
	board.Open(root)
	toml, _ := os.ReadFile(filepath.Join(root, "board.toml"))
	os.WriteFile(filepath.Join(root, "board.toml"), append(toml, []byte("\n[intake]\nlisten = \"127.0.0.1:0\"\ntoken = \"short\"\n")...), 0o644)
	if _, err := Start(root, Options{Port: -1}); err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Errorf("weak token accepted: %v", err)
	}
}

func TestM5REST(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	var projects []projectJSON
	br.do("GET", "/api/projects", nil, &projects)
	if projects[0].Suite == nil || projects[0].Suite.Words.Release != "build" {
		t.Fatalf("project suite = %+v", projects[0].Suite)
	}
	// Milestones.
	if code := br.do("PUT", "/api/projects/rpg/milestones", []map[string]string{{"name": "vertical-slice", "date": "2026-12-20"}, {"name": "alpha"}}, nil); code != 200 {
		t.Fatalf("set milestones = %d", code)
	}
	if code := br.do("PUT", "/api/projects/rpg/milestones", []map[string]string{{"name": "x", "date": "20/12/2026"}}, nil); code != 400 {
		t.Errorf("bad date = %d", code)
	}
	br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"milestone": "vertical-slice"}, nil)
	var ms []milestoneSummary
	br.do("GET", "/api/projects/rpg/milestones", nil, &ms)
	if len(ms) != 2 || ms[0].Name != "vertical-slice" || ms[0].Date != "2026-12-20" || ms[0].Total != 1 || ms[1].Total != 0 {
		t.Errorf("summary = %+v", ms)
	}
	// Releases.
	var rel board.Release
	if code := br.do("POST", "/api/projects/rpg/releases", map[string]string{"id": "b2026-11-19.1", "target": "Win64"}, &rel); code != 200 || rel.ID != "b2026-11-19.1" {
		t.Errorf("record = %d %+v", code, rel)
	}
	var rels []board.Release
	br.do("GET", "/api/projects/rpg/releases", nil, &rels)
	if len(rels) != 1 {
		t.Errorf("releases = %+v", rels)
	}
	// Pipeline advance by a person.
	br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"pipeline": map[string]string{"name": "vfx", "at": "concept"}}, nil)
	var adv map[string]string
	if code := br.do("POST", "/api/tasks/RPG-1/advance", map[string]string{"from": "concept"}, &adv); code != 200 || adv["at"] != "niagara" {
		t.Errorf("advance = %d %v", code, adv)
	}
	// A second click on the same "Next step" (still from concept) must not skip a step.
	if code := br.do("POST", "/api/tasks/RPG-1/advance", map[string]string{"from": "concept"}, nil); code != 409 {
		t.Errorf("repeated advance = %d, want 409", code)
	}
	// Editing a suite file tells the page.
	events := sse(t, br)
	os.MkdirAll(filepath.Join(s.Board.Root(), "suites"), 0o755)
	os.WriteFile(filepath.Join(s.Board.Root(), "suites", "README.txt"), []byte("notes"), 0o644)
	waitEvent(t, events, "suites event", func(e Event) bool { return e.Kind == "suites" })
	tk, _ := p.Get("RPG-1")
	if tk.Log[len(tk.Log)-1].Text != "asset vfx: concept → niagara" {
		t.Errorf("log = %q", tk.Log[len(tk.Log)-1].Text)
	}
	// Feedback from the UI.
	var fb map[string]string
	if code := br.do("POST", "/api/feedback", map[string]string{"project": "rpg", "text": "UI feedback"}, &fb); code != 200 || fb["kind"] != "playtest note" {
		t.Errorf("feedback = %d %v", code, fb)
	}
	// Switching the suite.
	os.MkdirAll(filepath.Join(s.Board.Root(), "suites"), 0o755)
	os.WriteFile(filepath.Join(s.Board.Root(), "suites", "webapp.toml"), []byte("[words]\nrelease = \"deploy\"\n"), 0o644)
	if code := br.do("PUT", "/api/projects/rpg/suite", map[string]string{"suite": "nope"}, nil); code != 400 {
		t.Errorf("unknown suite = %d", code)
	}
	if code := br.do("PUT", "/api/projects/rpg/suite", map[string]string{"suite": "webapp"}, nil); code != 200 {
		t.Errorf("set suite = %d", code)
	}
	br.do("GET", "/api/projects", nil, &projects)
	if projects[1].Slug != "rpg" || projects[1].Suite.Words.Release != "deploy" {
		t.Errorf("after switch = %+v", projects)
	}
}

// TestTaskJSONUsesLowercaseKeys guards the page's contract: claim and pipeline come as {by, at} and {name, at}.
func TestTaskJSONUsesLowercaseKeys(t *testing.T) {
	s, p := startWithProject(t)
	p.Claim("RPG-1", board.Actor{Kind: "agent", Session: "claude-code:abc"})
	p.Update("RPG-1", board.Actor{Kind: "person"}, board.Edit{Pipeline: &board.Pipeline{Name: "prop", At: "model"}}, "", nil)
	br := newBrowser(t, s)
	var raw map[string]any
	br.do("GET", "/api/tasks/RPG-1", nil, &raw)
	claim, _ := raw["claim"].(map[string]any)
	pl, _ := raw["pipeline"].(map[string]any)
	if claim["by"] != "claude-code:abc" || pl["name"] != "prop" || pl["at"] != "model" {
		t.Errorf("claim = %v, pipeline = %v", raw["claim"], raw["pipeline"])
	}
}

func TestWarningsListSuiteProblems(t *testing.T) {
	s, _ := startWithProject(t)
	os.MkdirAll(filepath.Join(s.Board.Root(), "suites"), 0o755)
	os.WriteFile(filepath.Join(s.Board.Root(), "suites", "webapp.toml"), []byte("[adapters]\nassets = \"s3\"\n"), 0o644)
	var w struct {
		Suites string `json:"suites"`
	}
	newBrowser(t, s).do("GET", "/api/warnings", nil, &w)
	if !strings.Contains(w.Suites, `unknown asset resolver "s3"`) {
		t.Errorf("warnings.suites = %q", w.Suites)
	}
}

func TestSuiteFileEditor(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	var f struct {
		Builtin, Exists bool
		Text, Version   string
	}
	if code := br.do("GET", "/api/suites/gamedev-unreal/file", nil, &f); code != 200 || !f.Builtin || f.Exists || f.Text != "" {
		t.Errorf("get before = %d %+v", code, f)
	}
	// A file that does not load is refused and not written.
	if code := br.do("PUT", "/api/suites/gamedev-unreal/file", map[string]string{"text": "[[views]]\nname = \"x\"\nlanes = \"sideways\""}, nil); code != 400 {
		t.Errorf("bad put = %d", code)
	}
	path := filepath.Join(s.Board.Root(), "suites", "gamedev-unreal.toml")
	if _, err := os.Stat(path); err == nil {
		t.Error("bad file written")
	}
	if code := br.do("PUT", "/api/suites/../file", map[string]string{"text": ""}, nil); code == 200 {
		t.Error("bad name accepted")
	}
	// A good file is written as typed, comments and all, and takes effect.
	text := "# our wording\n[words]\nrelease = \"drop\" # not build\n"
	if code := br.do("PUT", "/api/suites/gamedev-unreal/file", map[string]string{"text": text}, nil); code != 200 {
		t.Fatalf("put = %d", code)
	}
	br.do("GET", "/api/suites/gamedev-unreal/file", nil, &f)
	if !f.Exists || f.Text != text || f.Version == "" {
		t.Errorf("get after = %+v", f)
	}
	// A save from an editor opened before someone else's change is refused, not written over theirs.
	if code := br.do("PUT", "/api/suites/gamedev-unreal/file", map[string]string{"text": "[words]\nrelease = \"x\"\n", "version": ""}, nil); code != 409 {
		t.Errorf("stale save = %d, want 409", code)
	}
	if data, _ := os.ReadFile(path); string(data) != text {
		t.Errorf("stale save wrote the file:\n%s", data)
	}
	var saved struct{ Version string }
	if code := br.do("PUT", "/api/suites/gamedev-unreal/file", map[string]string{"text": text + "# more\n", "version": f.Version}, &saved); code != 200 || saved.Version == "" || saved.Version == f.Version {
		t.Errorf("current save = %d %+v", code, saved)
	}
	var list struct {
		Suites []suite.Suite `json:"suites"`
	}
	br.do("GET", "/api/suites", nil, &list)
	if len(list.Suites) == 0 || list.Suites[0].Words.Release != "drop" {
		t.Errorf("suites = %+v", list.Suites)
	}
	// Reset goes back to the built-in suite; deleting twice is fine.
	for range 2 {
		if code := br.do("DELETE", "/api/suites/gamedev-unreal/file", nil, nil); code != 200 {
			t.Errorf("delete = %d", code)
		}
	}
	br.do("GET", "/api/suites", nil, &list)
	if list.Suites[0].Words.Release != "build" {
		t.Errorf("after reset = %+v", list.Suites[0].Words)
	}
}

func TestSuiteAddViewAndTemplate(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	path := filepath.Join(s.Board.Root(), "suites", "gamedev-unreal.toml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("# keep me\n"), 0o644)
	if code := br.do("POST", "/api/suites/gamedev-unreal/views", map[string]any{"name": "Audio", "filter": "discipline:audio", "fields": []string{"labels"}, "layout": "table"}, nil); code != 200 {
		t.Fatalf("add view = %d", code)
	}
	if code := br.do("POST", "/api/suites/gamedev-unreal/templates", map[string]any{"name": "Sound bug", "title": "Sound: ", "discipline": "audio", "labels": []string{"bug"}}, nil); code != 200 {
		t.Fatalf("add template = %d", code)
	}
	// Taken names and bad values are refused; nothing changes.
	before, _ := os.ReadFile(path)
	if code := br.do("POST", "/api/suites/gamedev-unreal/views", map[string]any{"name": "audio"}, nil); code != 400 {
		t.Errorf("duplicate view = %d", code)
	}
	if code := br.do("POST", "/api/suites/gamedev-unreal/templates", map[string]any{"name": "x", "status": "done"}, nil); code != 400 {
		t.Errorf("bad template = %d", code)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) || !strings.HasPrefix(string(after), "# keep me\n") {
		t.Errorf("file = %s", after)
	}
	var list struct {
		Suites []suite.Suite `json:"suites"`
	}
	br.do("GET", "/api/suites", nil, &list)
	g := list.Suites[0]
	if _, ok := g.Template("Sound bug"); !ok || g.Views[len(g.Views)-1].Name != "Audio" || g.Views[0].Name != "Triage" {
		t.Errorf("views %v templates %v", g.Views, g.TemplateNames())
	}
	if _, ok := g.Template("Bug"); !ok {
		t.Error("built-in templates dropped")
	}
}
