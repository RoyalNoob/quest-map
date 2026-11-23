package board

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestAgentsMaySetDoneSetting(t *testing.T) {
	for _, tc := range []struct {
		toml string
		want bool
	}{
		{"", false}, // template written: default false
		{"[agents]\nmay_set_done = true\n", true},
		{"[agents]\nmay_set_done = false\n", false},
	} {
		root := t.TempDir()
		if tc.toml != "" {
			os.WriteFile(filepath.Join(root, "board.toml"), []byte(tc.toml), 0o644)
		}
		b, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if got := b.Config().Agents.MaySetDone; got != tc.want {
			t.Errorf("%q: may_set_done = %v, want %v", tc.toml, got, tc.want)
		}
	}
}

func TestDiffHandEditFindsInsertedNotesAndMovedFrom(t *testing.T) {
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	before := &Task{Log: []LogEntry{{Time: at, Who: "me", Text: "one"}, {Time: at.Add(time.Minute), Who: "me", Text: "two"}}}
	after := &Task{MovedFrom: "OLD-1", Log: []LogEntry{{Time: at, Who: "me", Text: "one"}, {Time: at, Who: "me", Text: "inserted"}, {Time: at.Add(time.Minute), Who: "me", Text: "two"}}}
	h := DiffHandEdit(before, after)
	if !slices.Equal(h.Notes, []string{"inserted"}) || !slices.Contains(h.Fields, "moved_from") {
		t.Errorf("diff = %+v", h)
	}
	// One line removed and one added in the same save: the added one still counts.
	after = &Task{Log: []LogEntry{{Time: at, Who: "me", Text: "one"}, {Time: at.Add(2 * time.Minute), Who: "me", Text: "three"}}}
	if h := DiffHandEdit(before, after); !slices.Equal(h.Notes, []string{"three"}) {
		t.Errorf("replace diff = %+v", h)
	}
}

func TestUpdateIfChecksUnderTheLock(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := b.CreateProject("rpg", "RPG", "")
	tk, _ := p.Create(&Task{Title: "x"}, Actor{Kind: "person"}, nil)
	stop := errors.New("stop")
	if _, err := p.UpdateIf(tk.ID, Actor{Kind: "agent", Session: "a"}, Edit{}, "n", nil, func(*Task) error { return stop }); err != stop {
		t.Errorf("err = %v", err)
	}
	if _, err := p.UpdateIf(tk.ID, Actor{Kind: "agent", Session: "a"}, Edit{}, " ", nil, nil); err == nil {
		t.Error("empty update accepted")
	}
}

func TestClaimUnblockedIfRechecksBlockers(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := b.CreateProject("rpg", "RPG", "")
	me := Actor{Kind: "person"}
	blocker, _ := p.Create(&Task{Title: "decide"}, me, nil)
	work, _ := p.Create(&Task{Title: "work", Doer: Agent, BlockedBy: []string{blocker.ID, "NOPE-9"}}, me, nil)
	ok := func(*Task) error { return nil }
	if _, err := p.ClaimUnblockedIf(work.ID, Actor{Kind: "agent", Session: "a"}, ok); !errors.Is(err, ErrClaimLost) {
		t.Errorf("claimed a blocked task: %v", err)
	}
	done := Done
	p.Update(blocker.ID, me, Edit{Status: &done}, "", nil)
	if _, err := p.ClaimUnblockedIf(work.ID, Actor{Kind: "agent", Session: "a"}, ok); err != nil {
		t.Errorf("claim after the blocker is done: %v", err)
	}
	if _, err := p.ClaimIf(work.ID, Actor{Kind: "agent", Session: "b"}, ok); !errors.Is(err, ErrClaimLost) {
		t.Errorf("claimed another session's task: %v", err)
	}
}
