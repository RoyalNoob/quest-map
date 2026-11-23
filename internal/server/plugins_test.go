package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// startWithPlugins starts a server whose config folder (next to custom.css) holds the given folder plugins:
// folder name -> file name -> content.
func startWithPlugins(t *testing.T, folders map[string]map[string]string) (*Server, *board.Project) {
	t.Helper()
	root := t.TempDir()
	b, _ := board.Open(root)
	b.CreateProject("rpg", "RPG", "RPG")
	cfg := t.TempDir()
	for dir, files := range folders {
		for name, data := range files {
			p := filepath.Join(cfg, "plugins", dir, filepath.FromSlash(name))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(data), 0o644)
		}
	}
	s, err := Start(root, Options{Port: -1, CustomCSS: filepath.Join(cfg, "custom.css")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, _ := s.Board.Project("rpg")
	return s, p
}

const hello = `name = "hello"
version = "0.1.0"
title = "Hello"
[[views]]
id = "hi"
title = "Hi"
`

// Done-when (M10): a plugin dropped into the config folder adds a view without code changes; one that is
// broken shows in /api/warnings and breaks nothing else.
func TestFolderPluginsAndWarnings(t *testing.T) {
	s, _ := startWithPlugins(t, map[string]map[string]string{
		"hello":  {"plugin.toml": hello, "main.js": "export function activate(api) {}", "i18n/ja.json": "{}"},
		"broken": {"plugin.toml": "name = \"broken\"\ntitle = \"B\"\n[[views]]\nid = \"x\"\n"}, // no main.js
		"evil":   {"plugin.toml": strings.Replace(hello, `"hello"`, `"reports"`, 1)},           // folder name differs
	})
	br := newBrowser(t, s)
	var list struct {
		Plugins []struct {
			Name        string
			Builtin     bool
			Entry       string
			Langs       []string
			Permissions []string
			Views       []struct{ ID, Scope string }
		}
	}
	if code := br.do("GET", "/api/plugins", nil, &list); code != 200 {
		t.Fatal(code)
	}
	got := map[string]bool{}
	for _, p := range list.Plugins {
		got[p.Name] = p.Builtin
		if p.Name == "hello" && (p.Entry != "main.js" || len(p.Langs) != 1 || p.Views[0].Scope != "project") {
			t.Errorf("hello = %+v", p)
		}
	}
	if b, ok := got["reports"]; !ok || !b {
		t.Errorf("built-in reports missing: %v", got)
	}
	if b, ok := got["hello"]; !ok || b {
		t.Errorf("folder plugin hello missing: %v", got)
	}
	if _, ok := got["broken"]; ok {
		t.Error("broken plugin listed")
	}
	var w struct {
		Plugins []struct{ Plugin, Error string }
	}
	br.do("GET", "/api/warnings", nil, &w)
	if len(w.Plugins) != 2 {
		t.Errorf("plugin warnings = %+v, want broken and evil", w.Plugins)
	}
}

// Done-when (M10): a plugin cannot read the session cookie, call /api or load a remote URL. Its files carry a
// CSP that forces the sandbox (opaque origin: no cookies, no storage of the page), allows no connections and
// no file outside its own folder; the cookie is HttpOnly and SameSite=Strict, and /api refuses a request
// without it.
func TestPluginSandbox(t *testing.T) {
	s, _ := startWithPlugins(t, map[string]map[string]string{"hello": {"plugin.toml": hello, "main.js": "export function activate() {}"}})
	base := fmt.Sprintf("http://127.0.0.1:%d", s.Info().Port)
	for _, path := range []string{"/plugins/hello/main.js", "/plugins/hello/_qm/frame.html", "/plugins/hello/_qm/api.js", "/plugins/reports/main.mjs"} {
		resp, err := http.Get(base + path) // no cookie, no token: plugin files are public
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		if resp.StatusCode != 200 {
			t.Errorf("%s = %d", path, resp.StatusCode)
			continue
		}
		own := base + strings.Join(strings.SplitN(path, "/", 4)[:3], "/") + "/"
		for _, want := range []string{"sandbox allow-scripts allow-downloads", "connect-src 'none'", "default-src 'none'", "script-src " + own + ";"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", path, csp, want)
			}
		}
		if strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "https:") || strings.Contains(csp, "*") {
			t.Errorf("%s: CSP %q lets the plugin out", path, csp)
		}
		if ct := resp.Header.Get("Content-Type"); strings.HasSuffix(path, "js") && !strings.HasPrefix(ct, "text/javascript") {
			t.Errorf("%s: Content-Type %q", path, ct)
		}
	}
	for _, path := range []string{"/plugins/nope/_qm/frame.html", "/plugins/hello/../../board.toml", "/plugins/hello/missing.js"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("%s = 200", path)
		}
	}
	// What a sandboxed frame's fetch looks like: no cookie (opaque origin), Origin: null.
	req, _ := http.NewRequest("GET", base+"/api/tasks", nil)
	req.Header.Set("Origin", "null")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/tasks from a plugin = %d, want 401", resp.StatusCode)
	}
	req, _ = http.NewRequest("PUT", base+"/api/plugins/reports/storage/x.json", strings.NewReader("{}"))
	req.Header.Set("Origin", "null")
	br := newBrowser(t, s)
	req.AddCookie(br.cookie) // even with the cookie, a write from the null origin is refused
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("PUT from origin null = %d, want 403", resp.StatusCode)
	}
}

func TestPluginStorage(t *testing.T) {
	s, p := startWithPlugins(t, map[string]map[string]string{"hello": {"plugin.toml": hello, "main.js": ""}})
	br := newBrowser(t, s)
	events := sse(t, br)
	put := func(path, body string) int {
		req, _ := http.NewRequest("PUT", br.base+path, strings.NewReader(body))
		req.AddCookie(br.cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put("/api/plugins/reports/storage/canvas/items/a.json?project=rpg", `{"x":1}`); code != 200 {
		t.Fatalf("put = %d", code)
	}
	waitEvent(t, events, "storage event naming its key", func(e Event) bool {
		return e.Kind == "storage" && e.Project == "rpg" && e.ID == "reports" && e.Key == "canvas/items/a.json"
	})
	if _, err := os.Stat(filepath.Join(p.Dir(), "plugins", "reports", "canvas", "items", "a.json")); err != nil {
		t.Error(err)
	}
	put("/api/plugins/reports/storage/settings.json", `{"board":true}`) // board scope
	if _, err := os.Stat(filepath.Join(s.Board.Root(), "plugins", "reports", "settings.json")); err != nil {
		t.Error(err)
	}
	var keys []string
	br.do("GET", "/api/plugins/reports/storage?project=rpg&prefix=canvas/", nil, &keys)
	if len(keys) != 1 || keys[0] != "canvas/items/a.json" {
		t.Errorf("list = %v", keys)
	}
	var v map[string]int
	br.do("GET", "/api/plugins/reports/storage/canvas/items/a.json?project=rpg", nil, &v)
	if v["x"] != 1 {
		t.Errorf("get = %v", v)
	}
	resp, _ := http.Get(br.base + "/api/plugins/reports/storage/canvas/items/a.json?project=rpg")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("storage without the cookie = %d %s", resp.StatusCode, body)
	}
	for path, want := range map[string]int{
		"/api/plugins/reports/storage/../x.json":          404, // cleaned by the mux, then not a plugin route
		"/api/plugins/reports/storage/.hidden":            400,
		"/api/plugins/reports/storage/Upper.json":         400,
		"/api/plugins/hello/storage/x.json":               403, // hello did not ask for storage
		"/api/plugins/nope/storage/x.json":                404,
		"/api/plugins/reports/storage/x.json?project=zzz": 404,
		"/api/plugins/reports/storage/nul.json":           400, // a Windows device name
		"/api/plugins/reports/storage/a/con":              400,
		"/api/plugins/reports/storage/trailing.":          400, // Windows drops the dot
		"/api/plugins/reports/storage/settings.json/x":    409, // settings.json holds a value
		"/api/plugins/reports/storage/canvas?project=rpg": 409, // keys below it hold values
	} {
		if code := put(path, "{}"); code != want && !(want == 404 && code >= 300) {
			t.Errorf("PUT %s = %d, want %d", path, code, want)
		}
	}
	if code := put("/api/plugins/reports/storage/bad.json", "{not json"); code != 400 {
		t.Errorf("bad JSON = %d", code)
	}
	if code := br.do("DELETE", "/api/plugins/reports/storage/canvas/items/a.json?project=rpg", nil, nil); code != 200 {
		t.Errorf("delete = %d", code)
	}
	var gone any = "x"
	br.do("GET", "/api/plugins/reports/storage/canvas/items/a.json?project=rpg", nil, &gone)
	if gone != nil {
		t.Errorf("after delete = %v", gone)
	}
}

// api.activity follows moves: a task's records written before it moved come back under its new id and project.
func TestActivityFollowsMoves(t *testing.T) {
	s, p := startWithProject(t)
	art, _ := s.Board.Project("art")
	me := board.Actor{Kind: "person", Name: "me"}
	if _, err := p.Claim("RPG-1", board.Actor{Kind: "agent", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Board.Move(p, "RPG-1", art, me); err != nil {
		t.Fatal(err)
	}
	br := newBrowser(t, s)
	var recs []struct {
		Project string
		Kind    string
		Task    string
		To      string
	}
	br.do("GET", "/api/activity?project=art&follow=yes&kinds=status", nil, &recs)
	var doing bool
	for _, r := range recs {
		if r.Kind != "status" || r.Project != "art" {
			t.Errorf("record %+v", r)
		}
		if r.To == "doing" && r.Task == "ART-1" {
			doing = true
		}
	}
	if !doing {
		t.Errorf("records = %+v, want RPG-1's move to doing as ART-1", recs)
	}
	recs = nil
	br.do("GET", "/api/activity?project=rpg&follow=yes", nil, &recs)
	for _, r := range recs {
		if r.Task == "ART-1" || r.Task == "RPG-1" {
			t.Errorf("rpg has %+v after the move", r)
		}
	}
	if code := br.do("GET", "/api/activity?until=soon", nil, nil); code != 400 {
		t.Errorf("bad until = %d", code)
	}
}

// putRaw sends a PUT with a raw body as the UI page would.
func putRaw(t *testing.T, br *browser, path string, body []byte) int {
	t.Helper()
	req, _ := http.NewRequest("PUT", br.base+path, bytes.NewReader(body))
	req.AddCookie(br.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

// api.storage.putBlob / blobURL (M11): images only, 10 MB, same key space as values.
func TestPluginBlobs(t *testing.T) {
	s, p := startWithPlugins(t, nil)
	br := newBrowser(t, s)
	if code := putRaw(t, br, "/api/plugins/vision/blobs/c1/blobs/a.png?project=rpg", tinyPNG); code != 200 {
		t.Fatalf("put png = %d", code)
	}
	if _, err := os.Stat(filepath.Join(p.Dir(), "plugins", "vision", "c1", "blobs", "a.png")); err != nil {
		t.Error(err)
	}
	req, _ := http.NewRequest("GET", br.base+"/api/plugins/vision/blobs/c1/blobs/a.png?project=rpg", nil)
	req.AddCookie(br.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, tinyPNG) {
		t.Errorf("get = %d %s %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), len(got))
	}
	for name, body := range map[string][]byte{
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html": []byte("<!doctype html><script>1</script>"),
		"big":  append(append([]byte{}, tinyPNG...), make([]byte, 10<<20)...),
	} {
		if code := putRaw(t, br, "/api/plugins/vision/blobs/c1/blobs/"+name+"?project=rpg", body); code == 200 {
			t.Errorf("%s blob accepted", name)
		}
	}
	// A JSON value is not served as a blob.
	putRaw(t, br, "/api/plugins/vision/storage/c1/canvas.json?project=rpg", []byte(`{"name":"Art"}`))
	if code := br.do("GET", "/api/plugins/vision/blobs/c1/canvas.json?project=rpg", nil, nil); code != http.StatusUnsupportedMediaType {
		t.Errorf("json as blob = %d", code)
	}
	// And an image is not handed over as a JSON value.
	if code := br.do("GET", "/api/plugins/vision/storage/c1/blobs/a.png?project=rpg", nil, nil); code != http.StatusUnsupportedMediaType {
		t.Errorf("image as a value = %d", code)
	}
	if code := putRaw(t, br, "/api/plugins/reports/blobs/x.png?project=rpg", tinyPNG); code != 200 {
		t.Errorf("reports may store blobs too (storage permission) = %d", code)
	}
}

// Done-when (M11): two board copies that moved different items of one canvas merge with no conflict and
// show both changes.
func TestVisionItemsFromTwoCopiesMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// put writes items through a server on root, then stops it.
	put := func(root string, kv map[string]string) {
		t.Helper()
		s, err := Start(root, Options{Port: -1, CustomCSS: filepath.Join(t.TempDir(), "custom.css")})
		if err != nil {
			t.Fatal(err)
		}
		br := newBrowser(t, s)
		for k, v := range kv {
			if code := putRaw(t, br, "/api/plugins/vision/storage/"+k+"?project=rpg", []byte(v)); code != 200 {
				t.Fatalf("put %s = %d", k, code)
			}
		}
		s.Close()
	}
	a := t.TempDir()
	ba, _ := board.Open(a)
	ba.CreateProject("rpg", "RPG", "RPG")
	put(a, map[string]string{
		"c1/canvas.json":   `{"name":"Art","view":{"x":0,"y":0,"zoom":1}}`,
		"c1/items/n1.json": `{"type":"note","x":0,"y":0,"w":200,"h":120,"z":1,"text":"Fog"}`,
		"c1/items/n2.json": `{"type":"note","x":300,"y":0,"w":200,"h":120,"z":2,"text":"Rust"}`,
	})
	git(a, "init", "-q", "-b", "main")
	git(a, "add", "-A")
	git(a, "commit", "-q", "-m", "canvas")
	b := filepath.Join(t.TempDir(), "copy")
	git(a, "clone", "-q", a, b)
	put(a, map[string]string{"c1/items/n1.json": `{"type":"note","x":50,"y":60,"w":200,"h":120,"z":1,"text":"Fog"}`})
	git(a, "add", "-A")
	git(a, "commit", "-q", "-m", "move n1")
	put(b, map[string]string{"c1/items/n2.json": `{"type":"note","x":300,"y":400,"w":200,"h":120,"z":2,"text":"Rust"}`})
	git(b, "add", "-A")
	git(b, "commit", "-q", "-m", "move n2")
	git(b, "pull", "-q", "--no-rebase", "origin", "main") // fails the test on a conflict

	s, err := Start(b, Options{Port: -1, CustomCSS: filepath.Join(t.TempDir(), "custom.css")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	br := newBrowser(t, s)
	var n1, n2 struct{ X, Y int }
	br.do("GET", "/api/plugins/vision/storage/c1/items/n1.json?project=rpg", nil, &n1)
	br.do("GET", "/api/plugins/vision/storage/c1/items/n2.json?project=rpg", nil, &n2)
	if n1.X != 50 || n1.Y != 60 || n2.Y != 400 {
		t.Errorf("after merge n1 = %+v, n2 = %+v; want both moves", n1, n2)
	}
}
