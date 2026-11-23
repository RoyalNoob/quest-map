package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/mcpserver"
)

// browser is an HTTP client that behaves like the UI page: it loads / for the session cookie and sends its
// own Origin on writes.
type browser struct {
	t      *testing.T
	base   string
	cookie *http.Cookie
	origin string
}

func newBrowser(t *testing.T, s *Server) *browser {
	t.Helper()
	base := "http://127.0.0.1:" + strconv.Itoa(s.Info().Port)
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET / = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var c *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			c = ck
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %+v", c)
	}
	return &browser{t: t, base: base, cookie: c, origin: base}
}

func (b *browser) do(method, path string, body any, out any) int {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, b.base+path, rd)
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	if method != "GET" && b.origin != "" {
		req.Header.Set("Origin", b.origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func startWithProject(t *testing.T) (*Server, *board.Project) {
	t.Helper()
	root := t.TempDir()
	b, _ := board.Open(root)
	p, _ := b.CreateProject("rpg", "RPG", "RPG")
	b.CreateProject("art", "ART", "Art")
	p.Create(&board.Task{Title: "Bridge collision", Doer: board.Agent, Discipline: "code"}, board.Actor{Kind: "person", Name: "me"}, nil)
	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, _ = s.Board.Project("rpg")
	return s, p
}

func TestUIPageAndCookieAuth(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	var projects []projectJSON
	if code := br.do("GET", "/api/projects", nil, &projects); code != 200 || len(projects) != 2 {
		t.Fatalf("projects = %d %+v", code, projects)
	}
	for _, p := range projects {
		if want := map[string]int{"rpg": 1, "art": 0}[p.Slug]; p.Open != want {
			t.Errorf("%s open = %d, want %d", p.Slug, p.Open, want)
		}
	}
	for _, asset := range []string{"/app.js", "/app.css", "/icon.svg"} {
		resp, err := http.Get(br.base + asset)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("%s: %v %v", asset, err, resp.Status)
		}
		resp.Body.Close()
	}
	anon := &browser{t: t, base: br.base}
	if code := anon.do("GET", "/api/projects", nil, nil); code != 401 {
		t.Errorf("no cookie: %d", code)
	}
	forged := &browser{t: t, base: br.base, cookie: &http.Cookie{Name: sessionCookie, Value: "guess"}}
	if code := forged.do("GET", "/api/tasks/RPG-1", nil, nil); code != 401 {
		t.Errorf("wrong cookie: %d", code)
	}
}

// TestCrossOriginPostIsRejected is half of the M4 acceptance check.
func TestCrossOriginPostIsRejected(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	evil := *br
	evil.origin = "https://evil.example"
	if code := evil.do("POST", "/api/tasks/RPG-1/notes", map[string]string{"text": "pwned"}, nil); code != 403 {
		t.Errorf("cross-origin POST: %d", code)
	}
	if code := evil.do("DELETE", "/api/tasks/RPG-1", nil, nil); code != 403 {
		t.Errorf("cross-origin DELETE: %d", code)
	}
	tk, _ := p.Get("RPG-1")
	if tk.Log[len(tk.Log)-1].Text == "pwned" {
		t.Error("the note was written")
	}
	if code := br.do("POST", "/api/tasks/RPG-1/notes", map[string]string{"text": "same origin is fine"}, nil); code != 200 {
		t.Errorf("same-origin POST: %d", code)
	}
}

func TestRESTWrites(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	var created map[string]string
	if code := br.do("POST", "/api/tasks", map[string]any{"project": "rpg", "title": "Paint the gate", "doer": "person",
		"discipline": "art", "labels": []string{"env", "env", " "}}, &created); code != 200 || created["id"] != "RPG-2" {
		t.Fatalf("create = %d %v", code, created)
	}
	if code := br.do("PATCH", "/api/tasks/RPG-2", map[string]any{"priority": "high", "labels": []string{"env", "castle"}, "note": "bumped"}, nil); code != 200 {
		t.Errorf("edit = %d", code)
	}
	if code := br.do("PATCH", "/api/tasks/RPG-2", map[string]any{"priority": "p1"}, nil); code != 400 {
		t.Errorf("bad priority = %d", code)
	}
	if code := br.do("PATCH", "/api/tasks/RPG-2", map[string]any{"bogus": 1}, nil); code != 400 {
		t.Errorf("unknown field = %d", code)
	}
	if code := br.do("POST", "/api/tasks/RPG-1/usage", map[string]any{"tokens": -500, "time": "-5m", "note": "double counted"}, nil); code != 200 {
		t.Errorf("correction = %d", code)
	}
	var tj taskJSON
	br.do("GET", "/api/tasks/RPG-2", nil, &tj)
	if tj.Priority != board.High || strings.Join(tj.Labels, ",") != "env,castle" || tj.Log[len(tj.Log)-1].Text != "bumped" || tj.Log[len(tj.Log)-1].Who == "" {
		t.Errorf("task = %+v", tj)
	}
	created = nil
	br.do("POST", "/api/tasks", map[string]any{"project": "rpg", "title": "Gate mesh", "pipeline": map[string]string{"name": "prop", "at": "blockout"}}, &created)
	var gate taskJSON
	if br.do("GET", "/api/tasks/"+created["id"], nil, &gate); gate.Pipeline == nil || gate.Pipeline.Name != "prop" || gate.Pipeline.At != "blockout" {
		t.Errorf("created with pipeline = %+v", gate.Pipeline)
	}
	br.do("GET", "/api/tasks/RPG-1", nil, &tj)
	if tj.Spent.Total != -500 || tj.Spent.Seconds != -300 || len(tj.History) == 0 {
		t.Errorf("spent/history = %+v %+v", tj.Spent, tj.History)
	}
	if code := br.do("POST", "/api/tasks/RPG-2/move", map[string]string{"to": "art"}, &created); code != 200 || created["id"] != "ART-1" {
		t.Errorf("move = %d %v", code, created)
	}
	if code := br.do("PATCH", "/api/tasks/ART-1", map[string]any{"status": "done"}, nil); code != 200 {
		t.Errorf("done = %d", code)
	}
	if code := br.do("POST", "/api/tasks/ART-1/archive", nil, nil); code != 200 {
		t.Errorf("archive = %d", code)
	}
	if code := br.do("DELETE", "/api/tasks/ART-1", nil, nil); code != 200 {
		t.Errorf("delete = %d", code)
	}
	if code := br.do("GET", "/api/tasks/ART-1", nil, nil); code != 404 {
		t.Errorf("deleted task = %d", code)
	}
	var rows []map[string]any
	br.do("GET", "/api/tasks?project=art&archived=yes", nil, &rows)
	if len(rows) != 0 {
		t.Errorf("index still lists the deleted task: %v", rows)
	}
	_ = p
}

func TestAttachments(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	upload := func(name string, data []byte) (int, map[string]string) {
		req, _ := http.NewRequest("POST", br.base+"/api/tasks/RPG-1/attachments?name="+url.QueryEscape(name), bytes.NewReader(data))
		req.AddCookie(br.cookie)
		req.Header.Set("Origin", br.origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, out := upload("../../evil shot.png", png)
	if code != 200 || out["name"] != "evil_shot.png" || out["markdown"] != "![evil_shot.png](../attachments/RPG-1/evil_shot.png)" {
		t.Fatalf("upload = %d %v", code, out)
	}
	if _, err := os.Stat(p.Dir() + "/attachments/RPG-1/evil_shot.png"); err != nil {
		t.Errorf("file not saved: %v", err)
	}
	if _, out2 := upload("evil_shot.png", png); out2["name"] != "evil_shot-1.png" {
		t.Errorf("second upload name = %v", out2)
	}
	req, _ := http.NewRequest("GET", br.base+out["url"], nil)
	req.AddCookie(br.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("serve = %v %v", err, resp.Header)
	}
	resp.Body.Close()
	if code, _ := upload("big.bin", make([]byte, board.MaxAttachment+1)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize = %d", code)
	}
}

// sse reads change events from /api/events.
func sse(t *testing.T, br *browser) <-chan Event {
	t.Helper()
	req, _ := http.NewRequest("GET", br.base+"/api/events", nil)
	req.AddCookie(br.cookie)
	resp, err := (&http.Client{}).Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("events: %v %v", err, resp)
	}
	ch := make(chan Event, 32)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e Event
				if json.Unmarshal([]byte(data), &e) == nil {
					ch <- e
				}
			}
		}
		close(ch)
	}()
	return ch
}

func waitEvent(t *testing.T, ch <-chan Event, what string, match func(Event) bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("%s: stream closed", what)
			}
			if match(e) {
				return
			}
		case <-deadline:
			t.Fatalf("%s: no event within 3s", what)
		}
	}
}

// TestHumanAndAgentSeeEachOthersChanges is the other half of the M4 acceptance check: a person in the UI and
// an agent over MCP edit the same board, and each sees the other's changes live.
func TestHumanAndAgentSeeEachOthersChanges(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	events := sse(t, br)

	// The agent claims and writes; the UI gets events for it.
	agentC := mcpserver.Caller{ID: "claude-code:aaaaaa", Client: "claude-code/2.4.1"}
	if _, out, err := s.MCP.Claim(agentC, mcpserver.ClaimIn{Project: "rpg"}); err != nil || out.Claimed == nil {
		t.Fatalf("claim: %v %+v", err, out)
	}
	waitEvent(t, events, "agent claim", func(e Event) bool { return e.Kind == "task" && e.ID == "RPG-1" })
	var tj taskJSON
	br.do("GET", "/api/tasks/RPG-1", nil, &tj)
	if tj.Claim == nil || tj.Claim.By != "claude-code:aaaaaa" {
		t.Errorf("UI sees claim = %+v", tj.Claim)
	}

	// The person edits in the UI; the agent's next reply carries it.
	br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"body": "Use the LOD1 mesh."}, nil)
	waitEvent(t, events, "person edit", func(e Event) bool { return e.Kind == "task" && e.ID == "RPG-1" })
	text, _, err := s.MCP.Note(agentC, mcpserver.NoteIn{ID: "RPG-1", Text: "continuing"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "changed by a person since your claim") || !strings.Contains(text, "body") {
		t.Errorf("agent reply = %q", text)
	}

	// A hand edit of the file reaches the UI through the watcher.
	path := p.TaskPath("RPG-1", false)
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "title: Bridge collision", "title: Bridge collision (by hand)", 1)), 0o644)
	waitEvent(t, events, "hand edit", func(e Event) bool { return e.Kind == "task" && e.ID == "RPG-1" })
	br.do("GET", "/api/tasks/RPG-1", nil, &tj)
	if tj.Title != "Bridge collision (by hand)" {
		t.Errorf("title = %q", tj.Title)
	}
}

func TestCloseEndsEventStreams(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	events := sse(t, br)
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on the event stream")
	}
	for range events {
	}
}

// A drawer edit made on an old version of the task is refused, so it cannot overwrite someone else's change.
func TestEditRefusesStaleVersion(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	var shown struct {
		Version string `json:"version"`
	}
	if code := br.do("GET", "/api/tasks/RPG-1", nil, &shown); code != 200 || shown.Version == "" {
		t.Fatalf("show = %d, version %q", code, shown.Version)
	}
	// An agent changes the task after the person opened it.
	if _, err := p.Update("RPG-1", board.Actor{Kind: "agent", Name: "claude-code"}, board.Edit{Body: ptr("Agent notes.")}, "", nil); err != nil {
		t.Fatal(err)
	}
	if code := br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"body": "Mine.", "version": shown.Version}, nil); code != 409 {
		t.Fatalf("stale edit = %d, want 409", code)
	}
	if got, _ := p.Get("RPG-1"); got.Body != "Agent notes." {
		t.Fatalf("body = %q, the agent's change was overwritten", got.Body)
	}
	var fresh struct {
		Version string `json:"version"`
	}
	br.do("GET", "/api/tasks/RPG-1", nil, &fresh)
	if code := br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"body": "Mine.", "version": fresh.Version}, nil); code != 200 {
		t.Fatalf("current edit = %d", code)
	}
	// Without a version (the person chose to save over it) the edit goes through.
	if code := br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"title": "Bridge collision v2"}, nil); code != 200 {
		t.Fatalf("unversioned edit = %d", code)
	}
}

func ptr[T any](v T) *T { return &v }

func TestCustomCSS(t *testing.T) {
	root := t.TempDir()
	board.Open(root)
	css := filepath.Join(t.TempDir(), "custom.css")
	s, err := Start(root, Options{Port: -1, CustomCSS: css})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	get := func() (int, string) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/custom.css", s.Info().Port)) // no cookie needed
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(); code != 200 || body != "" {
		t.Errorf("without a file = %d %q", code, body)
	}
	os.WriteFile(css, []byte(".card { border-radius: 0 }"), 0o644)
	if code, body := get(); code != 200 || body != ".card { border-radius: 0 }" {
		t.Errorf("with a file = %d %q", code, body)
	}
}

func TestAutoArchive(t *testing.T) {
	root := t.TempDir()
	b, _ := board.Open(root)
	p, _ := b.CreateProject("rpg", "RPG", "RPG")
	me := board.Actor{Kind: "person", Name: "me"}
	done := board.Done
	// Done a month ago, done just now, and an old task that is not done.
	b.Now = func() time.Time { return time.Now().Add(-30 * 24 * time.Hour) }
	p.Create(&board.Task{Title: "old done"}, me, nil)
	p.Update("RPG-1", me, board.Edit{Status: &done}, "", nil)
	p.Create(&board.Task{Title: "old open"}, me, nil)
	b.Now = time.Now
	p.Create(&board.Task{Title: "new done"}, me, nil)
	p.Update("RPG-3", me, board.Edit{Status: &done}, "", nil)

	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	br := newBrowser(t, s)
	if code := br.do("PUT", "/api/projects/rpg/archive", map[string]int{"days": -1}, nil); code != 400 {
		t.Errorf("negative days = %d", code)
	}
	var out map[string]int
	if code := br.do("PUT", "/api/projects/rpg/archive", map[string]int{"days": 7}, &out); code != 200 || out["archived"] != 1 {
		t.Fatalf("set 7 days = %d %v", code, out)
	}
	sp, _ := s.Board.Project("rpg")
	if tk, _ := sp.Get("RPG-1"); tk == nil || !tk.Archived {
		t.Errorf("RPG-1 not archived: %+v", tk)
	}
	for _, id := range []string{"RPG-2", "RPG-3"} {
		if tk, _ := sp.Get(id); tk == nil || tk.Archived {
			t.Errorf("%s archived", id)
		}
	}
	if sp.Config().ArchiveDoneAfterDays != 7 {
		t.Errorf("setting = %d", sp.Config().ArchiveDoneAfterDays)
	}
}

// Table rows carry the task version, list edits never drop others' entries, and custom keys are checked.
func TestRowVersionListOpsAndFieldChecks(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	var rows []struct {
		ID      string
		Version string
	}
	br.do("GET", "/api/tasks?project=rpg", nil, &rows)
	var shown struct {
		Version string `json:"version"`
	}
	br.do("GET", "/api/tasks/RPG-1", nil, &shown)
	if len(rows) != 1 || rows[0].Version == "" || rows[0].Version != shown.Version {
		t.Fatalf("row version %v, task version %q", rows, shown.Version)
	}
	labels := []string{"anim"}
	p.Update("RPG-1", board.Actor{Kind: "agent", Name: "claude-code"}, board.Edit{Labels: &labels}, "", nil)
	if code := br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"priority": "high", "version": rows[0].Version}, nil); code != 409 {
		t.Errorf("stale row edit = %d, want 409", code)
	}
	if code := br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"add_labels": []string{"rig"}, "add_assignees": []string{"jo"}}, nil); code != 200 {
		t.Errorf("list op = %d", code)
	}
	if tk, _ := p.Get("RPG-1"); strings.Join(tk.Labels, ",") != "anim,rig" || strings.Join(tk.Assignees, ",") != "jo" {
		t.Errorf("labels %v assignees %v", tk.Labels, tk.Assignees)
	}
	for _, f := range []map[string]string{{"": "x"}, {"Bad Key": "y"}, {"not_in_suite": "z"}} {
		if code := br.do("POST", "/api/tasks", map[string]any{"project": "rpg", "title": "t", "fields": f}, nil); code != 400 {
			t.Errorf("create with fields %v = %d, want 400", f, code)
		}
	}
	if code := br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"fields": map[string]string{"not_in_suite": "z"}}, nil); code != 400 {
		t.Errorf("edit with an undeclared field = %d, want 400", code)
	}
	// A value left from a field the suite no longer declares can still be cleared.
	p.Update("RPG-1", board.Actor{Kind: "person", Name: "me"}, board.Edit{Custom: map[string]string{"old_field": "v"}}, "", nil)
	if code := br.do("PATCH", "/api/tasks/RPG-1", map[string]any{"fields": map[string]string{"old_field": ""}}, nil); code != 200 {
		t.Errorf("clearing an undeclared field = %d, want 200", code)
	}
}

// A project's working folder can be pinned per machine; it survives a restart and must be a working copy.
func TestWorkdirSetting(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".lore"), 0o755)
	os.WriteFile(filepath.Join(repo, ".lore", "config.toml"), []byte("remote_url = \"\"\n"), 0o644)
	if code := br.do("PUT", "/api/projects/rpg/workdir", map[string]string{"dir": t.TempDir()}, nil); code != 400 {
		t.Errorf("plain folder = %d, want 400", code)
	}
	if code := br.do("PUT", "/api/projects/rpg/workdir", map[string]string{"dir": repo}, nil); code != 200 {
		t.Fatalf("set = %d", code)
	}
	root := s.Board.Root()
	s.Close()
	s2, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	if dir, pinned := s2.MCP.Workdir("rpg"); !pinned || !strings.EqualFold(filepath.Clean(dir), filepath.Clean(repo)) {
		t.Errorf("after restart: %q pinned=%v", dir, pinned)
	}
}

// Rows and the drawer say which blockers are still open: done and archived ones drop out (spec §8.3).
func TestBlockedAndBlocks(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	for _, title := range []string{"doing one", "done one", "archived one"} { // RPG-2..4
		br.do("POST", "/api/tasks", map[string]any{"project": "rpg", "title": title}, nil)
	}
	br.do("PATCH", "/api/tasks/RPG-3", map[string]any{"status": "done"}, nil)
	br.do("PATCH", "/api/tasks/RPG-4", map[string]any{"status": "done"}, nil)
	br.do("POST", "/api/tasks/RPG-4/archive", nil, nil)
	if code := br.do("POST", "/api/tasks", map[string]any{"project": "rpg", "title": "waits",
		"blocked_by": []string{"RPG-2", "RPG-1", "RPG-3", "RPG-4"}}, nil); code != 200 { // RPG-5
		t.Fatalf("create = %d", code)
	}
	var rows []struct {
		ID      string
		Blocked []string
	}
	br.do("GET", "/api/tasks?project=rpg", nil, &rows)
	for _, r := range rows {
		want := map[string]string{"RPG-5": "RPG-2,RPG-1"}[r.ID] // in blocked_by order: the chip names the first
		if got := strings.Join(r.Blocked, ","); got != want {
			t.Errorf("%s Blocked = %q, want %q", r.ID, got, want)
		}
	}
	var tj taskJSON
	br.do("GET", "/api/tasks/RPG-5", nil, &tj)
	st := map[string]string{}
	for _, l := range tj.Blockers {
		st[l.ID] = l.Status
		if l.ID == "RPG-4" && !l.Archived {
			t.Errorf("RPG-4 not marked archived")
		}
	}
	if len(tj.Blockers) != 4 || st["RPG-3"] != "done" || st["RPG-1"] != "todo" {
		t.Errorf("Blockers = %+v", tj.Blockers)
	}
	br.do("GET", "/api/tasks/RPG-1", nil, &tj)
	if len(tj.Blocks) != 1 || tj.Blocks[0].ID != "RPG-5" || tj.Blocks[0].Title != "waits" {
		t.Errorf("Blocks = %+v", tj.Blocks)
	}
}

// Plugins count days in the board's zone (§8.5): /api/meta names it, "" for Local (the browser's own).
func TestBoardZone(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("no zone data")
	}
	for loc, want := range map[*time.Location]string{time.UTC: "UTC", tokyo: "Asia/Tokyo", time.Local: ""} {
		if got := boardZone(loc); got != want {
			t.Errorf("boardZone(%v) = %q, want %q", loc, got, want)
		}
	}
}
