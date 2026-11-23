package board

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestCheckBlockers(t *testing.T) {
	b, rpg := testBoard(t)
	art, err := b.CreateProject("art", "ART", "Art")
	if err != nil {
		t.Fatal(err)
	}
	r1, _ := rpg.Create(&Task{Title: "one"}, roy, nil)
	r2, _ := rpg.Create(&Task{Title: "two", BlockedBy: []string{r1.ID}}, roy, nil)
	a1, _ := art.Create(&Task{Title: "art", BlockedBy: []string{r2.ID}}, roy, nil)
	done, _ := rpg.Create(&Task{Title: "done"}, roy, nil)
	rpg.Update(done.ID, roy, Edit{Status: ptr(Done)}, "", nil)
	if err := rpg.Archive(done.ID, roy); err != nil {
		t.Fatal(err)
	}

	free, _ := rpg.Create(&Task{Title: "free"}, roy, nil)
	got, err := b.CheckBlockers(free.ID, []string{" art-1 ", "ART-1", "", done.ID})
	if err != nil || !slices.Equal(got, []string{"ART-1", done.ID}) {
		t.Errorf("normalize, other project, archived: %v %v", got, err)
	}
	if got, err := b.CheckBlockers("", []string{r1.ID}); err != nil || !slices.Equal(got, []string{r1.ID}) {
		t.Errorf("new task: %v %v", got, err)
	}
	for name, c := range map[string]struct {
		id   string
		list []string
		want string
	}{
		"missing":        {r2.ID, []string{"RPG-99"}, "no task RPG-99"},
		"unknown key":    {r2.ID, []string{"NOPE-1"}, "no task NOPE-1"},
		"not an id":      {r2.ID, []string{"bridge"}, "not a task id"},
		"self":           {r2.ID, []string{strings.ToLower(r2.ID)}, "blocked by itself"},
		"two-task cycle": {r1.ID, []string{r2.ID}, "RPG-1 → RPG-2 → RPG-1"},
		"cross-project":  {r1.ID, []string{a1.ID}, "RPG-1 → ART-1 → RPG-2 → RPG-1"},
	} {
		if _, err := b.CheckBlockers(c.id, c.list); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestMoveRetargetsBlockers(t *testing.T) {
	b, rpg := testBoard(t)
	art, err := b.CreateProject("art", "ART", "Art")
	if err != nil {
		t.Fatal(err)
	}
	target, _ := rpg.Create(&Task{Title: "model"}, roy, nil)
	waits, _ := rpg.Create(&Task{Title: "waits", BlockedBy: []string{target.ID, "RPG-77"}}, roy, nil)
	other, _ := art.Create(&Task{Title: "art waits", BlockedBy: []string{target.ID}}, roy, nil)
	closed, _ := rpg.Create(&Task{Title: "closed", BlockedBy: []string{target.ID}}, roy, nil)
	rpg.Update(closed.ID, roy, Edit{Status: ptr(Done)}, "", nil)

	moved, err := b.Move(rpg, target.ID, art, roy)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := rpg.Get(waits.ID)
	if !slices.Equal(w.BlockedBy, []string{moved.ID, "RPG-77"}) {
		t.Errorf("same project: %v", w.BlockedBy)
	}
	if last := w.Log[len(w.Log)-1].Text; last != "blocker "+target.ID+" moved to "+moved.ID {
		t.Errorf("log = %q", last)
	}
	if o, _ := art.Get(other.ID); !slices.Equal(o.BlockedBy, []string{moved.ID}) {
		t.Errorf("other project: %v", o.BlockedBy)
	}
	if c, _ := rpg.Get(closed.ID); !slices.Equal(c.BlockedBy, []string{target.ID}) {
		t.Errorf("done task changed: %v", c.BlockedBy)
	}
}

func TestMovedTaskNotFoundNamesNewID(t *testing.T) {
	b, rpg := testBoard(t)
	art, _ := b.CreateProject("art", "ART", "Art")
	tk, _ := rpg.Create(&Task{Title: "rock"}, roy, nil)
	moved, err := b.Move(rpg, tk.ID, art, roy)
	if err != nil {
		t.Fatal(err)
	}
	back, err := b.Move(art, moved.ID, rpg, roy) // a second hop: RPG-1 → ART-1 → RPG-2
	if err != nil {
		t.Fatal(err)
	}
	if got := b.MovedTo(strings.ToLower(tk.ID)); got != back.ID {
		t.Errorf("MovedTo = %q, want %s", got, back.ID)
	}
	_, err = rpg.Get(tk.ID)
	if !errors.Is(err, ErrNotFound) || !strings.HasSuffix(err.Error(), "it was moved to "+back.ID) {
		t.Errorf("Get = %v", err)
	}
	if _, err := rpg.UpdateIf(tk.ID, roy, Edit{}, "x", nil, nil); err == nil || !strings.Contains(err.Error(), "moved to "+back.ID) {
		t.Errorf("UpdateIf = %v", err)
	}
	if _, err := b.CheckBlockers("", []string{tk.ID}); err == nil || !strings.Contains(err.Error(), "moved to "+back.ID) {
		t.Errorf("CheckBlockers = %v", err)
	}
	if got := b.MovedTo("RPG-99"); got != "" {
		t.Errorf("never moved: %q", got)
	}
}
