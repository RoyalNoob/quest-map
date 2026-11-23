package board

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const handWritten = `---
id: RPG-12
title: Player falls through bridge collision
status: doing
doer: agent
claim: {by: "claude-code:7f3a", at: 2026-11-17T09:15:00Z}
priority: high # low | normal | high | urgent
discipline: code
milestone: vertical-slice
labels:
  - physics
  - bug
studio_ticket: JIRA-881 # unknown key, kept as is
asset: null
found_in: b2026-11-14.3
blocked_by: [RPG-9]
url: null
created: 2026-11-17T09:12:00Z
---

Repro: load ` + "`L_Bridge`" + `, sprint across the middle span.

<!-- quest-map:log -->
## Log
- 2026-11-17 10:40 claude-code:7f3a (+42k tok, +25m): Collision mesh missing on SM_Bridge_Mid; regenerating.
  Second line of the same entry.
`

func TestParseHandWritten(t *testing.T) {
	tk, err := Parse([]byte(handWritten))
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID != "RPG-12" || tk.Status != Doing || tk.Doer != Agent || tk.Priority != High {
		t.Errorf("fields: %+v", tk)
	}
	if tk.Claim == nil || tk.Claim.By != "claude-code:7f3a" || !tk.Claim.At.Equal(time.Date(2026, 11, 17, 9, 15, 0, 0, time.UTC)) {
		t.Errorf("claim = %+v", tk.Claim)
	}
	if strings.Join(tk.Labels, ",") != "physics,bug" || strings.Join(tk.BlockedBy, ",") != "RPG-9" {
		t.Errorf("lists: %v %v", tk.Labels, tk.BlockedBy)
	}
	if tk.Asset != "" || tk.URL != "" || tk.FoundIn != "b2026-11-14.3" {
		t.Errorf("strings: asset=%q url=%q found_in=%q", tk.Asset, tk.URL, tk.FoundIn)
	}
	if tk.Body != "Repro: load `L_Bridge`, sprint across the middle span." {
		t.Errorf("body = %q", tk.Body)
	}
	if len(tk.Log) != 1 || tk.Log[0].Who != "claude-code:7f3a" || tk.Log[0].Usage != "+42k tok, +25m" ||
		tk.Log[0].Text != "Collision mesh missing on SM_Bridge_Mid; regenerating.\nSecond line of the same entry." {
		t.Errorf("log = %+v", tk.Log)
	}
}

func TestRenderUnchangedHandWrittenKeepsBytes(t *testing.T) {
	tk, err := Parse([]byte(handWritten))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tk.Render()
	if err != nil {
		t.Fatal(err)
	}
	// Values are unchanged, so no value node is replaced; only yaml's own re-encoding may differ.
	for _, want := range []string{
		"studio_ticket: JIRA-881 # unknown key, kept as is",
		"priority: high # low | normal | high | urgent",
		"labels:\n  - physics\n  - bug\n",
		"Repro: load `L_Bridge`",
		"  Second line of the same entry.\n",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("render lost %q:\n%s", want, out)
		}
	}
}

func TestEditKeepsUnknownKeysAndComments(t *testing.T) {
	tk, _ := Parse([]byte(handWritten))
	tk.Priority = Urgent
	tk.Labels = []string{"physics"}
	tk.FixedIn = "b2026-11-20.1" // absent before: inserted after found_in
	out, err := tk.Render()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"priority: urgent # low | normal | high | urgent",
		"studio_ticket: JIRA-881 # unknown key, kept as is",
		"found_in: b2026-11-14.3\nfixed_in: b2026-11-20.1\n",
		"labels: [physics]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "moved_from") {
		t.Errorf("empty absent key was added:\n%s", s)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Priority != Urgent || back.FixedIn != "b2026-11-20.1" {
		t.Errorf("reparse: %+v", back)
	}
}

func TestCRLFAndBOMArePreserved(t *testing.T) {
	in := utf8BOM + strings.ReplaceAll(handWritten, "\n", "\r\n")
	tk, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	tk.appendLog(time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC), "person", "", "checked")
	out, _ := tk.Render()
	s := string(out)
	if !strings.HasPrefix(s, utf8BOM+"---\r\n") {
		t.Errorf("BOM/CRLF start lost: %q", s[:12])
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Error("bare LF found in CRLF file")
	}
	if !strings.HasSuffix(s, "- 2026-11-18 08:00 person: checked\r\n") {
		t.Errorf("appended line wrong: %q", s[len(s)-50:])
	}
}

func TestAppendLogAddsMissingMarker(t *testing.T) {
	in := "---\nid: A-1\ntitle: t\nstatus: todo\n---\n\nSome notes.\n## Log\n- typed by hand\n"
	tk, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	tk.appendLog(time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC), "agent", " (+1k tok)", "hello")
	out, _ := tk.Render()
	want := "---\nid: A-1\ntitle: t\nstatus: todo\n---\n\nSome notes.\n## Log\n- typed by hand\n\n" +
		LogMarker + "\n## Log\n- 2026-01-02 03:04 agent (+1k tok): hello\n"
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if len(tk.Log) != 1 || tk.Log[0].Text != "hello" || tk.Log[0].Usage != "+1k tok" {
		t.Errorf("hand-written ## Log in the body must not count: %+v", tk.Log)
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no front matter": "# just markdown\n",
		"not closed":      "---\nid: A-1\nstatus: todo\n",
		"bad yaml":        "---\nid: [A-1\nstatus: todo\n---\n",
		"missing id":      "---\ntitle: x\nstatus: todo\n---\n",
		"bad status":      "---\nid: A-1\nstatus: started\n---\n",
		"bad doer":        "---\nid: A-1\nstatus: todo\ndoer: robot\n---\n",
		"not a mapping":   "---\n- a\n- b\n---\n",
	} {
		_, err := Parse([]byte(in))
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%s: err = %v, want ParseError", name, err)
		}
	}
}
