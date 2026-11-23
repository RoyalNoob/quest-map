package board

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestDemoBoard builds a real board folder to look at and hand-edit. It only runs when QM_DEMO_DIR is set:
//
//	$env:QM_DEMO_DIR = "$env:TEMP\quest-map-demo"; mise exec -- go test ./internal/board -run TestDemoBoard -v
//
// First run: creates the board, two projects and tasks, with claims, notes, usage, a move and an archive.
// Later runs: change RPG-1's priority and add a note, so hand edits to RPG-1.md can be seen surviving (or
// being re-encoded) by a quest-map write.
func TestDemoBoard(t *testing.T) {
	root := os.Getenv("QM_DEMO_DIR")
	if root == "" {
		t.Skip("set QM_DEMO_DIR to build a demo board")
	}
	b, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	agent := Actor{Kind: "agent", Session: "claude-code:demo", Client: "claude-code/2.4.1", Model: "claude-opus-5-5"}
	me := Actor{Kind: "person", Name: "me"}

	rpg, err := b.Project("rpg")
	if errors.Is(err, ErrNotFound) {
		firstRun(t, b, agent, me)
	} else if err != nil {
		t.Fatal(err)
	} else {
		laterRun(t, rpg, me)
	}

	for _, slug := range []string{"rpg", "art"} {
		p, _ := b.Project(slug)
		tasks, errs := p.List()
		for _, e := range errs {
			t.Logf("unreadable: %v", e)
		}
		for _, tk := range tasks {
			s, _ := p.Spent(tk.ID)
			u := Usage{Tokens: s.Tokens, Time: s.Time}
			t.Logf("%-6s %-7s %-6s %-40q spent:%s", tk.ID, tk.Status, tk.Doer, tk.Title, u.Suffix())
		}
	}
	t.Logf("board: %s", root)
}

func firstRun(t *testing.T, b *Board, agent, me Actor) {
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	rpg, err := b.CreateProject("rpg", "RPG", "Demo RPG")
	if err != nil {
		t.Fatal(err)
	}
	art, err := b.CreateProject("art", "ART", "Demo art")
	if err != nil {
		t.Fatal(err)
	}

	bridge, err := rpg.Create(&Task{Title: "Player falls through bridge collision", Doer: Agent, Discipline: "code",
		Labels: []string{"physics", "bug"}, Body: "Repro: load `L_Bridge`, sprint across the middle span."}, me, nil)
	if err != nil {
		t.Fatal(err)
	}
	must(rpg.Claim(bridge.ID, agent))
	must(rpg.Note(bridge.ID, agent, "Collision mesh missing on SM_Bridge_Mid; regenerating.",
		&Usage{Tokens: Tokens{Input: 1200, Output: 8100, CacheRead: 31000, CacheCreation: 1700}, Time: 25 * time.Minute, Source: SourceMeasured}))
	must(rpg.Update(bridge.ID, agent, Edit{Status: ptrTo(Review)}, "Fixed; please check in PIE.", nil))

	rock, err := rpg.Create(&Task{Title: "Rock texture too shiny", Body: "Roughness looks wrong."}, me, nil)
	if err != nil {
		t.Fatal(err)
	}
	must(rpg.Note(rock.ID, me, "Belongs to art.", &Usage{Time: 5 * time.Minute}))
	must(b.Move(rpg, rock.ID, art, me))

	old, err := rpg.Create(&Task{Title: "Set up project", Priority: Low}, me, nil)
	if err != nil {
		t.Fatal(err)
	}
	must(rpg.Update(old.ID, me, Edit{Status: ptrTo(Done)}, "", nil))
	if err := rpg.Archive(old.ID, me); err != nil {
		t.Fatal(err)
	}
	if err := rpg.RecordUsage("", agent, &Usage{Tokens: Tokens{Total: 4000}, Source: SourceSelf}); err != nil {
		t.Fatal(err)
	}
	fmt.Println("First run done. Hand-edit projects/rpg/tasks/RPG-1.md, then run again.")
}

func laterRun(t *testing.T, rpg *Project, me Actor) {
	tk, err := rpg.Get("RPG-1")
	if err != nil {
		t.Fatalf("RPG-1: %v (a file that fails to parse is never overwritten)", err)
	}
	next := High
	if tk.Priority == High {
		next = Normal
	}
	if _, err := rpg.Update("RPG-1", me, Edit{Priority: &next}, "Priority changed by the demo.", nil); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RPG-1 priority → %s, note added.\n", next)
}

func ptrTo[T any](v T) *T { return &v }
