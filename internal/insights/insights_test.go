package insights

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// fixture opens a copy of testdata/board (eight RPG tasks, activity on 2026-11-10; see the hand calculation in
// TestFixtureMatchesHandCalculation).
func fixture(t *testing.T) (*board.Board, *board.Project, Prices) {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS("testdata/board")); err != nil {
		t.Fatal(err)
	}
	b, err := board.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.Project("rpg")
	if err != nil {
		t.Fatal(err)
	}
	prices, err := LoadPrices(dst)
	if err != nil {
		t.Fatal(err)
	}
	return b, p, prices
}

var fixtureNow = time.Date(2026, 11, 10, 17, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

// metric compares a figure with a hand-calculated value.
func metric(t *testing.T, name string, got, want any) {
	t.Helper()
	switch g := got.(type) {
	case *float64:
		w, _ := want.(*float64)
		if (g == nil) != (w == nil) || (g != nil && math.Abs(*g-*w) > 1e-9) {
			t.Errorf("%s = %v, want %v", name, deref(g), deref(w))
		}
	case float64:
		if math.Abs(g-want.(float64)) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, g, want)
		}
	default:
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// TestFixtureMatchesHandCalculation checks every metric against a hand calculation from
// testdata/board/projects/rpg/activity.jsonl (prices: m-big input $10/M, output $20/M; now 17:00).
//
// Agent tasks: RPG-1 (archived, done first pass, $2.00), RPG-2 (sent back once, done, $0.50 + $1.00 rework),
// RPG-3 (released, $0.30 waste), RPG-4 (to review with "cannot continue", self-report with a total only),
// RPG-5 (done first pass, then reopened; found b2 ≥ fixed b1 = regression; m-other unpriced, $0.20; its
// self-report is ignored because its session is measured), RPG-8 (claim taken over by s9, then stale).
// Person tasks: RPG-6 (code, done first pass), RPG-7 (art, doing). Overhead: $0.50 with no task.
func TestFixtureMatchesHandCalculation(t *testing.T) {
	b, p, prices := fixture(t)
	rep, err := Compute(b, p, prices, Options{Now: fixtureNow, ClaimTTL: 4 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(rep.Groups))
	}
	a, h := rep.Groups[0].Agent, rep.Groups[0].Person
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"agent tasks", a.Tasks, 6},
		{"multi model", a.MultiModel, 1}, // RPG-5
		// First-pass: RPG-1 and RPG-5 of RPG-1, -2, -5.
		{"decided", a.Decided, 3},
		{"first pass", a.FirstPass, 2},
		{"first pass rate", a.FirstPassRate, f(2.0 / 3)},
		// Rework: RPG-2 sent back once; 100k tokens, 20 min, $1.00 after its first review.
		{"reviewed", a.Reviewed, 4},
		{"rework moves", a.ReworkMoves, 1},
		{"rework tokens", a.ReworkTokens, int64(100000)},
		{"rework time", a.ReworkTimeS, int64(1200)},
		{"rework cost", a.ReworkCost, 1.0},
		// Reopened: RPG-5 of RPG-1, -2, -5.
		{"reached done", a.ReachedDone, 3},
		{"reopened", a.Reopened, 1},
		{"regressions", a.Regressions, 1},
		{"reopen rate", a.ReopenRate, f(1.0 / 3)},
		// Cost: done now RPG-1 ($2.00) and RPG-2 ($1.50).
		{"done", a.Done, 2},
		{"done cost", a.DoneCost, 3.5},
		{"cost per accepted", a.CostPerAccepted, f(1.75)},
		{"waste tasks", a.WasteTasks, 3}, // RPG-3, -4, -8
		{"waste", a.Waste, 0.3},
		{"cost", a.Cost, 4.0},
		{"tokens", a.Tokens, int64(365000)},
		// Abandonment: claims 1+2+1+1+1+2; RPG-3 released, RPG-4 cannot continue, RPG-8 taken over + stale.
		{"claims", a.Claims, 8},
		{"abandoned", a.Abandoned, 4},
		{"abandon rate", a.AbandonRate, f(0.5)},
		// Attention: review waits 30m, 2h, 3h01m, 6h → lower median 2h; one person note (RPG-2).
		{"review wait n", a.ReviewWaitN, 4},
		{"review wait median", a.ReviewWaitMedS, int64(7200)},
		{"person notes", a.PersonNotes, 1},
		{"notes per task", a.NotesPerTask, f(1.0 / 6)},
		// Cycle: claim→review 1h, 1h, 1h, 30m → 1h; review→done 2h, 4h, 30m → 2h.
		{"claim→review n", a.ClaimToReviewN, 4},
		{"claim→review median", a.ClaimToReviewMedS, int64(3600)},
		{"review→done n", a.ReviewToDoneN, 3},
		{"review→done median", a.ReviewToDoneMedS, int64(7200)},
		// Coverage: 5 of 6 worked tasks have usage; measured 150k+30k+30k of 365k.
		{"worked", a.Worked, 6},
		{"with usage", a.WithUsage, 5},
		{"coverage", a.Coverage, f(5.0 / 6)},
		{"measured tokens", a.MeasuredTokens, int64(210000)},
		{"self tokens", a.SelfTokens, int64(155000)},
		{"measured share", a.MeasuredShare, f(210.0 / 365)},

		// Person baseline.
		{"person tasks", h.Tasks, 2},
		{"person first pass", h.FirstPassRate, f(1)},
		{"person claims", h.Claims, 2},
		{"person abandoned", h.Abandoned, 0},
		{"person done", h.Done, 1},
		{"person cost per accepted", h.CostPerAccepted, f(0)},
		{"person review wait", h.ReviewWaitMedS, int64(1800)},
		{"person claim→review", h.ClaimToReviewMedS, int64(7200)},
		{"person coverage", h.Coverage, f(0)},
		{"person measured share", h.MeasuredShare, (*float64)(nil)},

		{"overhead", rep.Overhead, 0.5},
		{"unpriced", rep.Unpriced, []string{"(total only)", "m-other"}},
		{"queue", rep.Queue, []QueueDay{{Day: "2026-11-10", Depth: 1, ToReview: 5, ToDone: 3}}},
	} {
		metric(t, c.name, c.got, c.want)
	}
}

func TestBaselinePerDiscipline(t *testing.T) {
	b, p, prices := fixture(t)
	rep, err := Compute(b, p, prices, Options{Now: fixtureNow, ClaimTTL: 4 * time.Hour, By: "discipline"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Groups) != 2 || rep.Groups[0].Name != "code" || rep.Groups[1].Name != "art" {
		t.Fatalf("groups = %+v, want code then art", rep.Groups)
	}
	code, art := rep.Groups[0], rep.Groups[1]
	metric(t, "code agent tasks", code.Agent.Tasks, 4)
	metric(t, "code agent first pass", code.Agent.FirstPassRate, f(2.0/3))
	metric(t, "code agent cost per accepted", code.Agent.CostPerAccepted, f(1.75))
	metric(t, "code agent abandon", code.Agent.AbandonRate, f(2.0/6))
	metric(t, "code person tasks", code.Person.Tasks, 1)
	metric(t, "code person first pass", code.Person.FirstPassRate, f(1))
	metric(t, "art agent first pass", art.Agent.FirstPassRate, (*float64)(nil))
	metric(t, "art agent waste", art.Agent.Waste, 0.3)
	metric(t, "art agent abandon", art.Agent.AbandonRate, f(1))
	metric(t, "art person tasks", art.Person.Tasks, 1)
}

func TestSinceAndTask(t *testing.T) {
	b, p, prices := fixture(t)
	rep, err := Compute(b, p, prices, Options{Now: fixtureNow, Since: time.Date(2026, 11, 10, 13, 30, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	// RPG-2 (15:00) and RPG-5 (14:00) have activity after 13:30; RPG-4 (review), RPG-7 and RPG-8 (doing) are
	// open work waiting since before it. The overhead was at 10:50.
	metric(t, "since agent tasks", rep.Groups[0].Agent.Tasks, 4)
	metric(t, "since person tasks", rep.Groups[0].Person.Tasks, 1)
	metric(t, "since overhead", rep.Overhead, 0.0)
	metric(t, "since queue", rep.Queue[len(rep.Queue)-1].Depth, 1) // RPG-4 still waits in review

	rep, err = Compute(b, p, prices, Options{Now: fixtureNow, Task: "rpg-2"})
	if err != nil {
		t.Fatal(err)
	}
	metric(t, "one task", rep.Groups[0].Agent.Tasks, 1)
	metric(t, "one task overhead", rep.Overhead, 0.0) // usage with no task is not this task's
	metric(t, "one task rework", rep.Groups[0].Agent.ReworkMoves, 1)
}

func TestParseSince(t *testing.T) {
	now := fixtureNow
	for in, want := range map[string]time.Time{
		"":           {},
		"2026-11-01": time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		"30d":        now.AddDate(0, 0, -30),
		"2w":         now.AddDate(0, 0, -14),
		"12h":        now.Add(-12 * time.Hour),
	} {
		got, err := ParseSince(in, now, time.UTC)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "30dw", "30dd", "d", "-3d"} {
		if _, err := ParseSince(bad, now, time.UTC); err == nil {
			t.Errorf("ParseSince(%q) accepted", bad)
		}
	}
}

func TestLoadPricesMissing(t *testing.T) {
	ps, err := LoadPrices(filepath.Join(t.TempDir(), "none"))
	if err != nil || len(ps) != 0 {
		t.Fatalf("LoadPrices = %v, %v", ps, err)
	}
}
