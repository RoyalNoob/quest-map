package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// These tests go through the HTTP /mcp endpoint with the headers the stdio proxy sends, the way agents reach
// the board. One connection is one agent session; subagents of a client share their parent's connection.

type hdrTransport struct{ h http.Header }

func (t hdrTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.h {
		r.Header[k] = v
	}
	return http.DefaultTransport.RoundTrip(r)
}

// fixtureWith is newFixture with a board.toml and index options.
func fixtureWith(t *testing.T, boardToml string, opts index.Options) *fixture {
	t.Helper()
	root := t.TempDir()
	if boardToml != "" {
		os.WriteFile(filepath.Join(root, "board.toml"), []byte(boardToml), 0o644)
	}
	b, err := board.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.CreateProject("rpg", "RPG", "RPG")
	if err != nil {
		t.Fatal(err)
	}
	p.LinkRepo("git:github.com/acme/rpg")
	ix, err := index.Open(filepath.Join(root, ".cache", "index.sqlite"), b, opts)
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

// serve puts the fixture's service on an HTTP /mcp endpoint.
func (f *fixture) serve() string {
	srv := f.svc.MCPServer("test")
	hs := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	f.t.Cleanup(hs.Close)
	return hs.URL
}

type mcpAgent struct {
	t  *testing.T
	cs *mcp.ClientSession
	id string // the board session, learned from the reply marker
}

// connect opens one agent session with the proxy's headers; clientSession "" sends none.
func connect(t *testing.T, url, short, clientSession string) *mcpAgent {
	t.Helper()
	h := http.Header{}
	h.Set(HeaderSession, short)
	h.Set(HeaderCwd, repoDir+"/Source")
	if clientSession != "" {
		h.Set(HeaderClientSession, clientSession)
	}
	tr := &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: hdrTransport{h}}, DisableStandaloneSSE: true}
	client := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "2.4.1"}, nil)
	cs, err := client.Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &mcpAgent{t: t, cs: cs, id: "claude-code:" + short}
}

// call returns the reply text without the marker line, and whether it was a tool error. It checks that the
// marker names this agent's session.
func (a *mcpAgent) call(tool string, args map[string]any) (string, bool) {
	a.t.Helper()
	res, err := a.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		a.t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	text := b.String()
	i := strings.LastIndex(text, "\n"+SessionMarker)
	if i < 0 || text[i+1+len(SessionMarker):] != a.id {
		a.t.Fatalf("%s: reply without this session's marker: %q", tool, text)
	}
	return text[:i], res.IsError
}

func (a *mcpAgent) ok(tool string, args map[string]any) string {
	a.t.Helper()
	text, isErr := a.call(tool, args)
	if isErr {
		a.t.Fatalf("%s: %s", tool, text)
	}
	return text
}

func claimedID(t *testing.T, text string) string {
	t.Helper()
	if !strings.HasPrefix(text, "Claimed ") {
		t.Fatalf("claim: %q", text)
	}
	return strings.Fields(text)[1]
}

func TestMCPSubagentsSharingASession(t *testing.T) {
	f := newFixture(t)
	f.task(&board.Task{Title: "A's task", Doer: board.Agent, Priority: board.High})
	f.task(&board.Task{Title: "B's task", Doer: board.Agent})
	f.rebuild()
	s := connect(t, f.serve(), "a1b2c3", "")
	a := claimedID(t, s.ok("task_claim", nil))
	b := claimedID(t, s.ok("task_claim", nil))

	// A person's note mid-work reaches only A's subagent: digest and warning, and it is not repeated.
	f.p.Note(a, me, "use the LOD1 mesh", nil)
	if r := s.ok("task_note", map[string]any{"id": b, "text": "B working"}); strings.Contains(r, "Updates:") || strings.Contains(r, "changed by a person") {
		t.Errorf("B got A's update: %q", r)
	}
	r := s.ok("task_note", map[string]any{"id": a, "text": "A working"})
	if !strings.HasPrefix(r, a+" changed by a person since your claim") || !strings.Contains(r, `person commented on `+a+`: "use the LOD1 mesh"`) {
		t.Errorf("A reply = %q", r)
	}
	if r := s.ok("task_note", map[string]any{"id": a, "text": "A again"}); strings.Contains(r, "Updates:") {
		t.Errorf("repeated: %q", r)
	}
	// The hand-off write still gets the warning, though it ends the claim.
	f.p.Note(a, me, "also check the collision", nil)
	r = s.ok("task_edit", map[string]any{"id": a, "status": "review", "note": "done; resume with task_show"})
	if !strings.HasPrefix(r, a+" changed by a person since your claim") || !strings.Contains(r, "also check the collision") || !strings.Contains(r, a+" review") {
		t.Errorf("hand-off reply = %q", r)
	}
}

func TestMCPConcurrentSiblingCalls(t *testing.T) {
	f := newFixture(t)
	f.task(&board.Task{Title: "shared", Doer: board.Agent})
	f.rebuild()
	s := connect(t, f.serve(), "c0ffee", "")
	a := claimedID(t, s.ok("task_claim", nil))
	f.p.Note(a, me, "one remark", nil)
	const n = 6
	replies := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replies[i] = s.ok("task_note", map[string]any{"id": a, "text": fmt.Sprint("sibling note ", i)})
		}()
	}
	wg.Wait()
	got := 0
	for _, r := range replies {
		got += strings.Count(r, "one remark")
	}
	if got != 1 {
		t.Errorf("the update was delivered %d times: %q", got, replies)
	}
	tk, _ := f.p.Get(a)
	for i := range n {
		found := false
		for _, e := range tk.Log {
			found = found || e.Text == fmt.Sprint("sibling note ", i)
		}
		if !found {
			t.Errorf("note %d lost", i)
		}
	}
}

func TestMCPClaimsOfAnotherSession(t *testing.T) {
	var shift atomic.Int64 // read by the index on the server's goroutines
	f := fixtureWith(t, "", index.Options{Now: func() time.Time { return time.Now().Add(time.Duration(shift.Load())) }})
	f.task(&board.Task{Title: "x's task", Doer: board.Agent})
	free := f.task(&board.Task{Title: "unclaimed", Doer: board.Agent, Status: board.Backlog})
	f.b.CreateProject("art", "ART", "Art")
	f.rebuild()
	url := f.serve()
	x, y := connect(t, url, "aaaaaa", ""), connect(t, url, "bbbbbb", "")
	id := claimedID(t, x.ok("task_claim", nil))
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"task_edit", map[string]any{"id": id, "title": "mine now"}},
		{"task_edit", map[string]any{"id": id, "status": "review"}},
		{"task_move", map[string]any{"id": id, "to": "art"}},
		{"task_release", map[string]any{"id": id}},
	} {
		if text, isErr := y.call(c.tool, c.args); !isErr || !strings.Contains(text, "not stale") {
			t.Errorf("%s of another session's claim: %q %v", c.tool, text, isErr)
		}
	}
	if text, isErr := y.call("task_note", map[string]any{"id": id, "text": "a note is fine"}); isErr {
		t.Errorf("note: %q", text)
	}
	if text, isErr := y.call("task_release", map[string]any{"id": free}); !isErr || !strings.Contains(text, "not claimed") {
		t.Errorf("release of an unclaimed task: %q", text)
	}
	// Once the claim is stale, others may take over.
	shift.Store(int64(5 * time.Hour))
	if text, isErr := y.call("task_release", map[string]any{"id": id, "note": "x went quiet"}); isErr {
		t.Errorf("release of a stale claim: %q", text)
	}
}

func TestMCPAgentsAndDone(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		f := fixtureWith(t, fmt.Sprintf("[agents]\nmay_set_done = %v\n", allowed), index.Options{})
		f.task(&board.Task{Title: "t", Doer: board.Agent})
		f.rebuild()
		a := connect(t, f.serve(), "dddddd", "")
		id := claimedID(t, a.ok("task_claim", nil))
		text, isErr := a.call("task_edit", map[string]any{"id": id, "status": "done"})
		if allowed == isErr {
			t.Errorf("may_set_done=%v: %q %v", allowed, text, isErr)
		}
		if !allowed && !strings.Contains(text, "review") {
			t.Errorf("error does not point to review: %q", text)
		}
		// A person is not limited by the setting.
		if !allowed {
			if _, err := f.p.Update(id, me, board.Edit{Status: ptr(board.Done)}, "", nil); err != nil {
				t.Errorf("person done: %v", err)
			}
		}
	}
}

// toolResultWith is a transcript tool_result line whose content is a reply text.
func toolResultWith(ts, text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":%q}]}]}}`, ts, text)
}

func TestMCPTranscriptLinkAttribution(t *testing.T) {
	f := newFixture(t)
	f.task(&board.Task{Title: "t", Doer: board.Agent})
	f.rebuild()
	a := connect(t, f.serve(), "eeeeee", "") // no client session header: the marker is the only link
	id := claimedID(t, a.ok("task_claim", nil))
	a.ok("task_note", map[string]any{"id": id, "text": "x", "spent_tokens": 50000, "spent_time": "10m"})

	dir := t.TempDir()
	main := filepath.Join(dir, "sess-a.jsonl")
	os.WriteFile(main, []byte(strings.Join([]string{
		// Resumed history from before the session used quest-map: not counted.
		promptLine("2026-11-18T08:00:00Z"),
		assistantLine("old", "claude-opus-5-5", "2026-11-18T08:01:00Z", 1000, 1000, 1000, 1000),
		promptLine("2026-11-18T10:00:00Z"),
		assistantLine("m1", "claude-opus-5-5", "2026-11-18T10:00:10Z", 10, 100, 1000, 0),
		toolResultWith("2026-11-18T10:00:20Z", "Claimed "+id+" doing\n"+SessionMarker+a.id),
		assistantLine("m2", "claude-opus-5-5", "2026-11-18T10:02:00Z", 5, 50, 0, 0),
		assistantLine("synth", "<synthetic>", "2026-11-18T10:02:01Z", 0, 0, 0, 0),
	}, "\n")+"\n"), 0o644)
	sub := filepath.Join(dir, "sess-a", "subagents", "agent-1.jsonl")
	os.MkdirAll(filepath.Dir(sub), 0o755)
	os.WriteFile(sub, []byte(`{"type":"user","isSidechain":true,"timestamp":"2026-11-18T10:00:30Z","message":{"content":"sub task"}}`+"\n"+
		assistantLine("s1", "claude-haiku-5", "2026-11-18T10:01:00Z", 1, 2, 3, 4)+"\n"), 0o644)

	res, err := f.svc.IngestTranscript(HookRequest{Client: "claude-code", ClientSession: "sess-a", TranscriptPath: main, Cwd: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Session != a.id || res.Task != id || res.Tokens != 1110+55+10 || res.Seconds != 120 {
		t.Errorf("result = %+v", res)
	}
	recs, _, _ := f.p.Activity()
	for _, r := range recs {
		if r.Kind == board.KindUsage && r.Source == board.SourceMeasured && r.Session != a.id {
			t.Errorf("measured record under session %q", r.Session)
		}
		if r.Model == "<synthetic>" {
			t.Errorf("zero-token message recorded: %+v", r)
		}
	}
	sp, _ := f.p.Spent(id)
	if sp.Tokens.Sum() != 1175 || sp.Time != 2*time.Minute {
		t.Errorf("spent = %+v (self-report not suppressed?)", sp)
	}

	// More subagent lines: counted once, from the subagent file's own cursor.
	fh, _ := os.OpenFile(sub, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(assistantLine("s2", "claude-haiku-5", "2026-11-18T10:03:00Z", 0, 100, 0, 0) + "\n")
	fh.Close()
	// The same file under another spelling of its path is not read from the start again.
	alt := strings.ToUpper(main[:1]) + main[1:]
	if res, err = f.svc.IngestTranscript(HookRequest{ClientSession: "sess-a", TranscriptPath: alt, Cwd: repoDir}); err != nil || res.Tokens != 100 {
		t.Errorf("second ingest = %+v %v", res, err)
	}

	// /clear: a new client session and transcript on the same MCP connection; the marker links it again.
	cleared := filepath.Join(dir, "sess-b.jsonl")
	os.WriteFile(cleared, []byte(strings.Join([]string{
		promptLine("2026-11-18T11:00:00Z"),
		toolResultWith("2026-11-18T11:00:05Z", "ok\n"+SessionMarker+a.id),
		assistantLine("b1", "claude-opus-5-5", "2026-11-18T11:00:30Z", 1, 1, 0, 0),
	}, "\n")+"\n"), 0o644)
	if res, err = f.svc.IngestTranscript(HookRequest{ClientSession: "sess-b", TranscriptPath: cleared, Cwd: repoDir}); err != nil || res.Session != a.id || res.Task != id || res.Tokens != 2 {
		t.Errorf("after /clear = %+v %v", res, err)
	}

	// A session that never talked to quest-map records nothing, even with a claimed task in its folder.
	n := len(recs)
	other := filepath.Join(dir, "sess-c.jsonl")
	os.WriteFile(other, []byte(promptLine("2026-11-18T12:00:00Z")+"\n"+assistantLine("c1", "claude-opus-5-5", "2026-11-18T12:01:00Z", 9, 9, 9, 9)+"\n"), 0o644)
	if res, err = f.svc.IngestTranscript(HookRequest{ClientSession: "sess-c", TranscriptPath: other, Cwd: repoDir}); err != nil || res.Session != "" || res.Task != "" || res.Project != "" {
		t.Errorf("unlinked = %+v %v", res, err)
	}
	recs, _, _ = f.p.Activity()
	for _, r := range recs[n:] {
		if r.Kind == board.KindUsage && r.Session != a.id {
			t.Errorf("unlinked session recorded: %+v", r)
		}
	}
}

func TestIngestRefusesOtherFiles(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	txt := filepath.Join(dir, "notes.txt")
	os.WriteFile(txt, []byte("x\n"), 0o644)
	for _, p := range []string{txt, dir + string(filepath.Separator) + "x.jsonl", "relative.jsonl"} {
		if _, err := f.svc.IngestTranscript(HookRequest{ClientSession: "s", TranscriptPath: p}); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
	os.Mkdir(filepath.Join(dir, "d.jsonl"), 0o755)
	if _, err := f.svc.IngestTranscript(HookRequest{ClientSession: "s", TranscriptPath: filepath.Join(dir, "d.jsonl")}); err == nil {
		t.Error("folder accepted")
	}
}

func TestAmbiguousFolderAsksForProject(t *testing.T) {
	f := newFixture(t)
	f.b.CreateProject("tools", "TL", "Tools")
	tl, _ := f.b.Project("tools")
	tl.LinkRepo("git:github.com/acme/tools")
	f.rebuild()
	f.svc.vcs = vcs.NewDetectorWith(func(string) [][2]string {
		return [][2]string{{"origin", "git@github.com:acme/rpg.git"}, {"tools", "git@github.com:acme/tools.git"}}
	})
	if _, _, err := f.svc.Find(agentCaller("aaaaaa"), FindIn{}); err == nil || !strings.Contains(err.Error(), "several projects") {
		t.Errorf("err = %v", err)
	}
}

func TestClaimRanksEmptyPriorityAsNormal(t *testing.T) {
	rows := []index.Row{{ID: "RPG-1", Priority: "low"}, {ID: "RPG-2", Priority: ""}, {ID: "RPG-3", Priority: "high"}}
	f := newFixture(t)
	f.svc.sortForClaim(f.p, rows)
	if rows[0].ID != "RPG-3" || rows[1].ID != "RPG-2" || rows[2].ID != "RPG-1" {
		t.Errorf("order = %v", rows)
	}
}

func TestModelArgumentIsChecked(t *testing.T) {
	f := newFixture(t)
	id := f.task(&board.Task{Title: "t", Doer: board.Agent})
	f.rebuild()
	if _, _, err := f.svc.Note(agentCaller("aaaaaa"), NoteIn{ID: id, Text: "x", UsageArgs: UsageArgs{Model: "bad model\nname"}}); err == nil {
		t.Error("bad model accepted")
	}
	if _, _, err := f.svc.Note(agentCaller("aaaaaa"), NoteIn{ID: id, Text: "x", UsageArgs: UsageArgs{Model: "claude-opus-5-5[1m]"}}); err != nil {
		t.Errorf("good model refused: %v", err)
	}
}

// structured calls a tool and returns its structured content.
func (a *mcpAgent) structured(tool string, args map[string]any) (map[string]any, bool) {
	a.t.Helper()
	res, err := a.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		a.t.Fatalf("%s: %v", tool, err)
	}
	m, _ := res.StructuredContent.(map[string]any)
	return m, res.IsError
}

// Clients that show only the structured content (Claude Code) must still get the reply text and the marker.
func TestMCPStructuredReplyCarriesMessage(t *testing.T) {
	f := newFixture(t)
	f.b.CreateProject("art", "ART", "Art")
	free := f.task(&board.Task{Title: "unclaimed", Doer: board.Agent, Status: board.Backlog})
	f.rebuild()
	a := connect(t, f.serve(), "abcabc", "")

	m, isErr := a.structured("task_claim", nil)
	msg, _ := m[messageField].(string)
	if isErr || !strings.Contains(msg, "No unblocked todo agent task") || !strings.HasSuffix(msg, SessionMarker+a.id) {
		t.Errorf("empty claim: %v %v", m, isErr)
	}
	// show and find: the fields hold the data, so the message does not repeat it.
	m, _ = a.structured("task_show", map[string]any{"id": free})
	if m["id"] != free || m[messageField] != SessionMarker+a.id {
		t.Errorf("show message = %v", m)
	}
	if text, _ := a.call("task_show", map[string]any{"id": free}); !strings.Contains(text, "unclaimed") {
		t.Errorf("show text = %q", text)
	}
	m, _ = a.structured("task_find", map[string]any{"filter": "status:review"})
	if msg, _ := m[messageField].(string); !strings.HasPrefix(msg, "No tasks in rpg match") {
		t.Errorf("empty find message = %v", m)
	}
	m, _ = a.structured("task_find", map[string]any{"filter": "status:backlog"})
	if m[messageField] != SessionMarker+a.id || len(m["tasks"].([]any)) != 1 {
		t.Errorf("find message = %v", m)
	}

	moved, _ := a.structured("task_move", map[string]any{"id": free, "to": "art"})
	newID := moved["task"].(map[string]any)["id"].(string)
	text, isErr := a.call("task_show", map[string]any{"id": free})
	if !isErr || !strings.Contains(text, "it was moved to "+newID) {
		t.Errorf("show of a moved id: %q", text)
	}
	text, isErr = a.call("task_release", map[string]any{"id": newID})
	if !isErr || strings.Contains(text, "not allowed") || !strings.Contains(text, "nothing to release") {
		t.Errorf("release wording: %q", text)
	}
}
