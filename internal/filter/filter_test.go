package filter

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	q, err := Parse(`status:doing -status:done label:"art review" -"exact phrase" bridge  asset:SM_Bridge* Doer:agent 10:30`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Term{
		{Key: "status", Value: "doing"},
		{Neg: true, Key: "status", Value: "done"},
		{Key: "label", Value: "art review"},
		{Neg: true, Value: "exact phrase"},
		{Value: "bridge"},
		{Key: "asset", Value: "SM_Bridge*"},
		{Key: "doer", Value: "agent"},
		{Value: "10:30"},
	}
	if len(q.Terms) != len(want) {
		t.Fatalf("terms = %+v", q.Terms)
	}
	for i := range want {
		if q.Terms[i] != want[i] {
			t.Errorf("term %d = %+v, want %+v", i, q.Terms[i], want[i])
		}
	}
	if s := q.String(); s != `status:doing -status:done label:"art review" -"exact phrase" bridge asset:SM_Bridge* doer:agent "10:30"` {
		t.Errorf("String() = %s", s)
	}
	if !q.Has("label") || q.Has("stale") {
		t.Error("Has")
	}
}

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]string{
		"stauts:doing":   "unknown filter",
		"status:started": "status:",
		"blocked:maybe":  "yes or no",
		"label:":         "needs a value",
		`label:"open`:    "unclosed quote",
		"doer:robot":     "person or agent",
		"priority:p1":    "low, normal",
	} {
		_, err := Parse(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) err = %v, want it to mention %q", in, err, want)
		}
	}
}

func TestParseEmpty(t *testing.T) {
	q, err := Parse("   ")
	if err != nil || len(q.Terms) != 0 {
		t.Errorf("%+v %v", q, err)
	}
}

func TestParseEdgeCases(t *testing.T) {
	q, err := Parse(`Status:Doing blocked:Yes https://example.com/x 橋:当たり判定 - "" -""`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Term{
		{Key: "status", Value: "doing"},
		{Key: "blocked", Value: "yes"},
		{Value: "https://example.com/x"},
		{Value: "橋:当たり判定"},
	}
	if len(q.Terms) != len(want) {
		t.Fatalf("terms = %+v", q.Terms)
	}
	for i := range want {
		if q.Terms[i] != want[i] {
			t.Errorf("term %d = %+v, want %+v", i, q.Terms[i], want[i])
		}
	}
	if _, err := Parse("found:latest/"); err == nil {
		t.Error("found:latest/ accepted")
	}
}

func TestStringRoundTrips(t *testing.T) {
	for _, in := range []Term{
		{Value: "status:doing"}, {Value: "-x"}, {Neg: true, Value: "two words"}, {Key: "label", Value: "a:b"},
		{Value: "https://example.com"}, {Key: "milestone", Value: "-early"},
	} {
		q, err := Parse(Query{Terms: []Term{in}}.String())
		if err != nil || len(q.Terms) != 1 || q.Terms[0] != in {
			t.Errorf("%+v → %q → %+v %v", in, Query{Terms: []Term{in}}.String(), q.Terms, err)
		}
	}
}
