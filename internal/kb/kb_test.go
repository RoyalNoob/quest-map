package kb

import (
	"slices"
	"testing"
)

func TestParseQuery(t *testing.T) {
	q, err := ParseQuery(`player falls tag:physics source:Source/Collision stale:yes "error: null" retired:yes`)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.Words, []string{"player", "falls", "error: null"}) || !slices.Equal(q.Tags, []string{"physics"}) ||
		q.Source != "Source/Collision" || q.Stale != "yes" || !q.Retired {
		t.Errorf("query = %+v", q)
	}
	if _, err := ParseQuery("stale:maybe"); err == nil {
		t.Error("stale:maybe accepted")
	}
}

func TestUnder(t *testing.T) {
	files := []string{"Source/Collision/BridgeGen.cpp", "Source/Other.cpp", "Content/Props/SM_Bridge.uasset"}
	got := under(files, []string{"source/collision/", "Content/*/SM_Bridge.uasset"})
	if !slices.Equal(got, []string{"Source/Collision/BridgeGen.cpp", "Content/Props/SM_Bridge.uasset"}) {
		t.Errorf("under = %v", got)
	}
	if got := under(files, []string{"Source/Coll"}); len(got) != 0 {
		t.Errorf("a name prefix matched: %v", got)
	}
}

func TestValidRev(t *testing.T) {
	for rev, want := range map[string]bool{"3f9c2e1": true, "3F9C2E1A0b": true, "--output=x": false, "HEAD": false, "abc": false, "3f9c2e1 --x": false} {
		if validRev.MatchString(rev) != want {
			t.Errorf("validRev(%q) = %v", rev, !want)
		}
	}
}
