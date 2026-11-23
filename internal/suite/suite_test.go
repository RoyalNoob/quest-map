package suite

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinGameDev(t *testing.T) {
	r, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Get("")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "gamedev-unreal" || s.Words.Release != "build" || s.Words.Item != "asset" || !s.HasDiscipline("Art") {
		t.Errorf("suite = %+v", s)
	}
	if p, ok := s.Pipeline("prop"); !ok || p.Steps[0] != "blockout" || p.Steps[len(p.Steps)-1] != "in-engine" {
		t.Errorf("prop pipeline = %+v", p)
	}
	if s.Intake.Discipline != "qa" || s.Resolver().Name() != "unreal" || strings.Join(s.Adapters.Locks, ",") != "git-lfs,lore" {
		t.Errorf("intake/adapters = %+v %+v", s.Intake, s.Adapters)
	}
	if len(s.Views) < 3 || s.Views[0].Name != "Triage" || s.Views[0].Lanes != "discipline" || s.Views[0].Sort != "priority" {
		t.Errorf("views = %+v", s.Views)
	}
	if b, ok := s.Template("bug"); !ok || b.Title != "Bug: " || !strings.Contains(b.Body, "Steps to reproduce") || b.Labels[0] != "bug" {
		t.Errorf("bug template = %+v", b)
	}
	if p, ok := s.Template("Prop asset"); !ok || p.Pipeline != "prop" || p.Discipline != "art" {
		t.Errorf("prop template = %+v", p)
	}
}

func TestTemplatesValidate(t *testing.T) {
	for name, body := range map[string]string{
		"no name":      "[[templates]]\ntitle = \"x\"",
		"bad status":   "[[templates]]\nname = \"a\"\nstatus = \"doing\"",
		"bad doer":     "[[templates]]\nname = \"a\"\ndoer = \"robot\"",
		"bad priority": "[[templates]]\nname = \"a\"\npriority = \"asap\"",
		"bad pipeline": "[[templates]]\nname = \"a\"\npipeline = \"vehicle\"",
		"twice":        "[[templates]]\nname = \"a\"\n[[templates]]\nname = \"A\"",
	} {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "suites"), 0o755)
		os.WriteFile(filepath.Join(root, "suites", "x.toml"), []byte(body), 0o644)
		if _, err := Load(root); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	// A template in a board file may use a pipeline from the built-in suite it extends.
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "suites"), 0o755)
	os.WriteFile(filepath.Join(root, "suites", "gamedev-unreal.toml"), []byte("[[templates]]\nname = \"FX\"\npipeline = \"vfx\""), 0o644)
	r, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := r.Get("")
	if strings.Join(g.TemplateNames(), ",") != "FX" {
		t.Errorf("templates = %v", g.TemplateNames())
	}
}

func TestViewsExtendAndValidate(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "suites"), 0o755)
	// A board file that sets views replaces the built-in list; other keys stay.
	os.WriteFile(filepath.Join(root, "suites", "gamedev-unreal.toml"), []byte(`
[[views]]
name = "Art"
filter = "discipline:art"
fields = ["steps", "labels"]
layout = "table"
`), 0o644)
	r, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := r.Get("")
	if len(g.Views) != 1 || g.Views[0].Layout != "table" || len(g.Pipelines) != 4 {
		t.Errorf("views = %+v", g.Views)
	}
	for name, body := range map[string]string{
		"bad filter": "[[views]]\nname = \"a\"\nfilter = \"status:nope\"",
		"bad lanes":  "[[views]]\nname = \"a\"\nlanes = \"owner\"",
		"bad sort":   "[[views]]\nname = \"a\"\nsort = \"size\"",
		"bad field":  "[[views]]\nname = \"a\"\nfields = [\"colour\"]",
		"no name":    "[[views]]\nfilter = \"status:todo\"",
		"twice":      "[[views]]\nname = \"a\"\n[[views]]\nname = \"A\"",
	} {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "suites"), 0o755)
		os.WriteFile(filepath.Join(root, "suites", "x.toml"), []byte(body), 0o644)
		if _, err := Load(root); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestBoardFileExtendsAndDefines(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "suites"), 0o755)
	// Extend the built-in suite: only the keys set change.
	os.WriteFile(filepath.Join(root, "suites", "gamedev-unreal.toml"), []byte(`
release_targets = ["Win64", "PS5"]
[words]
feedback = "bug report"
`), 0o644)
	// A new suite on top of the generic defaults.
	os.WriteFile(filepath.Join(root, "suites", "webapp.toml"), []byte(`
title = "Web app"
disciplines = ["frontend", "backend", "qa"]
[words]
release = "release"
item = "feature"
[[pipelines]]
name = "feature"
steps = ["spec", "build", "review", "ship"]
`), 0o644)
	r, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := r.Get("gamedev-unreal")
	if strings.Join(g.ReleaseTargets, ",") != "Win64,PS5" || g.Words.Feedback != "bug report" || g.Words.Release != "build" || len(g.Pipelines) != 4 {
		t.Errorf("extended = %+v", g)
	}
	w, err := r.Get("webapp")
	if err != nil {
		t.Fatal(err)
	}
	if w.Words.Item != "feature" || w.Words.Target != "target" || w.Resolver().Name() != "path" || w.Intake.Status != "backlog" || len(w.Pipelines) != 1 {
		t.Errorf("new suite = %+v", w)
	}
	if strings.Join(r.Names(), ",") != "gamedev-unreal,webapp" {
		t.Errorf("names = %v", r.Names())
	}
}

func TestBadSuiteFiles(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":      `colour = "red"`,
		"unknown resolver": "[adapters]\nassets = \"unity\"",
		"unknown lock":     "[adapters]\nlocks = [\"svn\"]",
		"empty pipeline":   "[[pipelines]]\nname = \"x\"\nsteps = []",
		"bad toml":         "disciplines = [",
	} {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "suites"), 0o755)
		os.WriteFile(filepath.Join(root, "suites", "x.toml"), []byte(body), 0o644)
		if _, err := Load(root); err == nil {
			t.Errorf("%s: loaded", name)
		} else if !strings.Contains(err.Error(), "x.toml") {
			t.Errorf("%s: error does not name the file: %v", name, err)
		}
	}
	r, _ := Load(t.TempDir())
	if _, err := r.Get("nope"); err == nil || !strings.Contains(err.Error(), "gamedev-unreal") {
		t.Errorf("unknown suite err = %v", err)
	}
}

// TestLangLabels: the built-in suite has Japanese and Traditional Chinese labels, and a board file extends one
// language's labels per key without dropping the others (spec §4.4).
func TestLangLabels(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "suites"), 0o755)
	os.WriteFile(filepath.Join(root, "suites", "gamedev-unreal.toml"), []byte(`
[lang.ja.disciplines]
art = "美術"

[lang.de.wording]
release = "Build"
`), 0o644)
	r, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Get("gamedev-unreal")
	if err != nil {
		t.Fatal(err)
	}
	ja, tw, de := s.Lang["ja"], s.Lang["zh-TW"], s.Lang["de"]
	if ja.Disciplines["art"] != "美術" || ja.Disciplines["code"] != "コード" || ja.Wording["release"] != "ビルド" {
		t.Errorf("ja = %+v", ja)
	}
	if tw.Steps["blockout"] == "" || tw.Templates["Bug"] == "" || tw.Fields["platform"] != "平台" {
		t.Errorf("zh-TW = %+v", tw)
	}
	if de.Wording["release"] != "Build" {
		t.Errorf("de = %+v", de)
	}
	for _, d := range s.Disciplines {
		if ja.Disciplines[d] == "" || tw.Disciplines[d] == "" {
			t.Errorf("discipline %s has no ja or zh-TW label", d)
		}
	}
	for _, p := range s.Pipelines {
		for _, st := range p.Steps {
			if ja.Steps[st] == "" || tw.Steps[st] == "" {
				t.Errorf("step %s has no ja or zh-TW label", st)
			}
		}
	}
	os.WriteFile(filepath.Join(root, "suites", "x.toml"), []byte("[lang.\"../x\".wording]\nrelease = \"b\"\n"), 0o644)
	if r, err := Load(root); err == nil {
		if _, err := r.Get("x"); err == nil {
			t.Error("a [lang] table with a bad language code loaded")
		}
	}
}

func TestCheck(t *testing.T) {
	for name, c := range map[string]struct{ suite, body string }{
		"bad name":       {"Web App", ""},
		"bad toml":       {"webapp", "[words"},
		"unknown key":    {"webapp", "colour = \"red\""},
		"bad view":       {"gamedev-unreal", "[[views]]\nname = \"x\"\nlanes = \"sideways\""},
		"twice pipeline": {"webapp", "[[pipelines]]\nname = \"a\"\nsteps = [\"x\"]\n[[pipelines]]\nname = \"a\"\nsteps = [\"y\"]"},
	} {
		if err := Check(c.suite, []byte(c.body)); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
	// A template may use a pipeline of the built-in suite it extends; a new suite starts empty.
	if err := Check("gamedev-unreal", []byte("# notes\n[[templates]]\nname = \"FX\"\npipeline = \"vfx\"")); err != nil {
		t.Error(err)
	}
	if err := Check("webapp", []byte("[[templates]]\nname = \"FX\"\npipeline = \"vfx\"")); err == nil {
		t.Error("new suite used a built-in pipeline")
	}
	if err := Check("webapp", nil); err != nil {
		t.Error(err)
	}
	if !IsBuiltin("gamedev-unreal") || IsBuiltin("webapp") || IsBuiltin("../x") {
		t.Error("IsBuiltin")
	}
}

func TestAdd(t *testing.T) {
	// A file without views gets the built-in views first, so adding one keeps them; comments stay.
	file := []byte("# our wording\n[words]\nrelease = \"drop\"")
	out, err := Add("gamedev-unreal", file, View{Name: "Audio", Filter: "discipline:audio", Layout: "table"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), string(file)+"\n") {
		t.Errorf("file changed:\n%s", out)
	}
	s, err := withFile("gamedev-unreal", out)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Views); n < 4 || s.Views[0].Name != "Triage" || s.Views[n-1].Name != "Audio" || s.Views[n-1].Layout != "table" {
		t.Errorf("views = %+v", s.Views)
	}
	if strings.Contains(string(out), "lanes = ''") || strings.Contains(string(out), `lanes = ""`) {
		t.Errorf("empty keys written:\n%s", out)
	}
	// Once the file sets views, only the new one is added.
	out2, err := Add("gamedev-unreal", out, View{Name: "Art", Filter: "discipline:art"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(out2), "[[views]]") - strings.Count(string(out), "[[views]]"); got != 1 {
		t.Errorf("added %d blocks", got)
	}
	// A name already in the list, or a bad entry, is refused.
	if _, err := Add("gamedev-unreal", out2, View{Name: "art"}); err == nil {
		t.Error("duplicate view added")
	}
	if _, err := Add("gamedev-unreal", nil, Template{Name: "X", Status: "doing"}); err == nil {
		t.Error("bad template added")
	}
	// A list written inline cannot take a block: the error says what to change.
	inline := []byte("views = [{name = \"Mine\", filter = \"mine\"}]\n")
	if _, err := Add("gamedev-unreal", inline, View{Name: "Art"}); err == nil || !strings.Contains(err.Error(), "[[views]] block") {
		t.Errorf("inline list: %v", err)
	}
	// Templates, with a multi-line body, into a new suite.
	out3, err := Add("webapp", nil, Template{Name: "Bug", Title: "Bug: ", Body: "Steps:\n1.\n", Labels: []string{"bug"}})
	if err != nil {
		t.Fatal(err)
	}
	s, _ = withFile("webapp", out3)
	if len(s.Templates) != 1 || s.Templates[0].Body != "Steps:\n1.\n" || s.Templates[0].Labels[0] != "bug" {
		t.Errorf("templates = %+v\n%s", s.Templates, out3)
	}
}

// [show] names fields that can be hidden and simple terms the browser can check; a file's table replaces the
// built-in one (spec §4.1).
func TestShow(t *testing.T) {
	r, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := r.Get("gamedev-unreal")
	if !slices.Contains(g.Show["found_in"], "label:bug") || len(g.Show["asset"]) == 0 {
		t.Errorf("built-in show = %v", g.Show)
	}
	for name, body := range map[string]string{
		"built-in key":   "[show]\ntitle = [\"label:x\"]",
		"unknown key":    "[show]\nbogus = [\"label:x\"]",
		"index-only key": "[show]\nfound_in = [\"blocked:yes\"]",
		"two terms":      "[show]\nfound_in = [\"label:a label:b\"]",
		"free word":      "[show]\nfound_in = [\"bridge\"]",
		"bad value":      "[show]\nfound_in = [\"status:sleeping\"]",
	} {
		if err := Check("gamedev-unreal", []byte(body)); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
	data := "[show]\nfound_in = [\"-label:feature\", \"platform:Win64\"]\nue_map = [\"discipline:level\"]\nurl = []"
	if err := Check("gamedev-unreal", []byte(data)); err != nil {
		t.Fatal(err)
	}
	s, _ := withFile("gamedev-unreal", []byte(data))
	if len(s.Show) != 3 || s.Show["asset"] != nil {
		t.Errorf("file did not replace the table: %v", s.Show)
	}

	// A later file that replaces [[fields]] without its own [show] drops the inherited rules and terms that name
	// a field it no longer has, instead of failing (a suite that fails to load takes every suite with it).
	s = generic.clone()
	base := "[[fields]]\nkey = \"platform\"\n[[fields]]\nkey = \"ue_map\"\n" +
		"[show]\nue_map = [\"discipline:level\"]\nfound_in = [\"platform:pc\", \"label:bug\"]\nfixed_in = [\"platform:pc\"]\n"
	if err := s.merge([]byte(base)); err != nil {
		t.Fatal(err)
	}
	if err := s.merge([]byte("[[fields]]\nkey = \"team\"\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.validate(); err != nil {
		t.Fatalf("inherited rules for a dropped field: %v", err)
	}
	if want := map[string][]string{"found_in": {"label:bug"}}; !reflect.DeepEqual(s.Show, want) {
		t.Errorf("show after fields replaced = %v, want %v", s.Show, want)
	}
}
