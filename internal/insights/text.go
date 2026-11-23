package insights

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// row is one metric line: its text value and its sample size.
type row struct {
	name string
	val  func(m *Metrics) (string, int)
}

func pct(r *float64) string {
	if r == nil {
		return "–"
	}
	return strconv.FormatFloat(*r*100, 'f', 0, 64) + "%"
}

func usd(v float64) string { return "$" + strconv.FormatFloat(v, 'f', 2, 64) }

func usdp(r *float64) string {
	if r == nil {
		return "–"
	}
	return usd(*r)
}

func dur(s int64, n int) string {
	if n == 0 {
		return "–"
	}
	return board.FormatDuration(time.Duration(s) * time.Second)
}

var rows = []row{
	{"first-pass acceptance", func(m *Metrics) (string, int) { return pct(m.FirstPassRate), m.Decided }},
	{"cost per accepted task", func(m *Metrics) (string, int) { return usdp(m.CostPerAccepted), m.Done }},
	{"waste (abandoned, not done)", func(m *Metrics) (string, int) { return usd(m.Waste), m.WasteTasks }},
	{"rework: moves back from review", func(m *Metrics) (string, int) { return strconv.Itoa(m.ReworkMoves), m.Reviewed }},
	{"rework: spent after first review", func(m *Metrics) (string, int) {
		if m.Reviewed == 0 {
			return "–", 0
		}
		return fmt.Sprintf("%s tok, %s, %s", board.FormatTokens(m.ReworkTokens), board.FormatDuration(time.Duration(m.ReworkTimeS)*time.Second), usd(m.ReworkCost)), m.Reviewed
	}},
	{"reopened after done", func(m *Metrics) (string, int) {
		s := pct(m.ReopenRate)
		if m.Regressions > 0 {
			s += fmt.Sprintf(" (%d regression)", m.Regressions)
		}
		return s, m.ReachedDone
	}},
	{"abandoned claims", func(m *Metrics) (string, int) { return pct(m.AbandonRate), m.Claims }},
	{"review wait (median)", func(m *Metrics) (string, int) { return dur(m.ReviewWaitMedS, m.ReviewWaitN), m.ReviewWaitN }},
	{"person notes per task", func(m *Metrics) (string, int) {
		if m.NotesPerTask == nil {
			return "–", 0
		}
		return strconv.FormatFloat(*m.NotesPerTask, 'f', 1, 64), m.Tasks
	}},
	{"cycle: claim → review (median)", func(m *Metrics) (string, int) { return dur(m.ClaimToReviewMedS, m.ClaimToReviewN), m.ClaimToReviewN }},
	{"cycle: review → done (median)", func(m *Metrics) (string, int) { return dur(m.ReviewToDoneMedS, m.ReviewToDoneN), m.ReviewToDoneN }},
	{"coverage: tasks with usage", func(m *Metrics) (string, int) { return pct(m.Coverage), m.Worked }},
	{"coverage: measured tokens", func(m *Metrics) (string, int) { return pct(m.MeasuredShare), m.WithUsage }},
	{"total cost", func(m *Metrics) (string, int) { return usd(m.Cost), m.Tasks }},
}

func cell(m *Metrics, r row) string {
	v, n := r.val(m)
	return fmt.Sprintf("%s [%d]", v, n)
}

// WriteText writes the report as aligned text. Sample sizes are in brackets; groups under FewTasks tasks
// are marked "few".
func (r *Report) WriteText(w io.Writer) error {
	period := "all time"
	if !r.Since.IsZero() {
		period = "since " + r.Since.Format("2006-01-02 15:04")
	}
	fmt.Fprintf(w, "Insights for %s (%s), %s", r.Project, r.Key, period)
	if r.By != "" {
		fmt.Fprintf(w, ", by %s", r.By)
	}
	fmt.Fprintf(w, ".\n[n] = sample size; \"few\" = under %d tasks, read with care.\n", FewTasks)
	if len(r.Groups) == 0 {
		fmt.Fprintln(w, "\nNo task activity in this period.")
	}
	for _, g := range r.Groups {
		fmt.Fprintln(w)
		if g.Name != "" {
			fmt.Fprintf(w, "== %s ==\n", g.Name)
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "\tagent\tperson (baseline)\n")
		fmt.Fprintf(tw, "tasks\t%s\t%s\n", count(g.Agent.Tasks, g.Agent.MultiModel), count(g.Person.Tasks, 0))
		for _, row := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", row.name, cell(&g.Agent, row), cell(&g.Person, row))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "\nOverhead (usage with no task): %s\n", usd(r.Overhead))
	if len(r.Unpriced) > 0 {
		fmt.Fprintf(w, "Not priced (counted as $0; add [prices.<model>] to board.toml): %s\n", strings.Join(r.Unpriced, ", "))
	}
	if n := len(r.Queue); n > 0 {
		q := r.Queue[n-1]
		fmt.Fprintf(w, "Agent review queue today: %d waiting, %d moved to review, %d to done.\n", q.Depth, q.ToReview, q.ToDone)
	}
	return nil
}

func count(n, multi int) string {
	s := strconv.Itoa(n)
	if n < FewTasks {
		s += " (few)"
	}
	if multi > 0 {
		s += fmt.Sprintf(", %d multi-model", multi)
	}
	return s
}

// csvColumns are the raw figures, one CSV row per group and doer.
var csvColumns = []struct {
	name string
	val  func(m *Metrics) string
}{
	{"tasks", func(m *Metrics) string { return itoa(m.Tasks) }},
	{"multi_model", func(m *Metrics) string { return itoa(m.MultiModel) }},
	{"decided", func(m *Metrics) string { return itoa(m.Decided) }},
	{"first_pass", func(m *Metrics) string { return itoa(m.FirstPass) }},
	{"first_pass_rate", func(m *Metrics) string { return rate(m.FirstPassRate) }},
	{"reviewed", func(m *Metrics) string { return itoa(m.Reviewed) }},
	{"rework_moves", func(m *Metrics) string { return itoa(m.ReworkMoves) }},
	{"rework_tokens", func(m *Metrics) string { return i64(m.ReworkTokens) }},
	{"rework_time_s", func(m *Metrics) string { return i64(m.ReworkTimeS) }},
	{"rework_usd", func(m *Metrics) string { return money(m.ReworkCost) }},
	{"reached_done", func(m *Metrics) string { return itoa(m.ReachedDone) }},
	{"reopened", func(m *Metrics) string { return itoa(m.Reopened) }},
	{"regressions", func(m *Metrics) string { return itoa(m.Regressions) }},
	{"reopen_rate", func(m *Metrics) string { return rate(m.ReopenRate) }},
	{"done", func(m *Metrics) string { return itoa(m.Done) }},
	{"done_usd", func(m *Metrics) string { return money(m.DoneCost) }},
	{"cost_per_accepted", func(m *Metrics) string { return rate(m.CostPerAccepted) }},
	{"waste_tasks", func(m *Metrics) string { return itoa(m.WasteTasks) }},
	{"waste_usd", func(m *Metrics) string { return money(m.Waste) }},
	{"cost_usd", func(m *Metrics) string { return money(m.Cost) }},
	{"tokens", func(m *Metrics) string { return i64(m.Tokens) }},
	{"claims", func(m *Metrics) string { return itoa(m.Claims) }},
	{"abandoned", func(m *Metrics) string { return itoa(m.Abandoned) }},
	{"abandon_rate", func(m *Metrics) string { return rate(m.AbandonRate) }},
	{"review_wait_n", func(m *Metrics) string { return itoa(m.ReviewWaitN) }},
	{"review_wait_median_s", func(m *Metrics) string { return i64(m.ReviewWaitMedS) }},
	{"person_notes", func(m *Metrics) string { return itoa(m.PersonNotes) }},
	{"claim_to_review_n", func(m *Metrics) string { return itoa(m.ClaimToReviewN) }},
	{"claim_to_review_median_s", func(m *Metrics) string { return i64(m.ClaimToReviewMedS) }},
	{"review_to_done_n", func(m *Metrics) string { return itoa(m.ReviewToDoneN) }},
	{"review_to_done_median_s", func(m *Metrics) string { return i64(m.ReviewToDoneMedS) }},
	{"worked", func(m *Metrics) string { return itoa(m.Worked) }},
	{"with_usage", func(m *Metrics) string { return itoa(m.WithUsage) }},
	{"measured_tokens", func(m *Metrics) string { return i64(m.MeasuredTokens) }},
	{"self_tokens", func(m *Metrics) string { return i64(m.SelfTokens) }},
}

func itoa(n int) string      { return strconv.Itoa(n) }
func i64(n int64) string     { return strconv.FormatInt(n, 10) }
func money(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
func rate(r *float64) string {
	if r == nil {
		return ""
	}
	return strconv.FormatFloat(*r, 'f', 4, 64)
}

// WriteCSV writes one row per group and doer with the raw figures.
func (r *Report) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	head := []string{"project", "by", "group", "doer"}
	for _, c := range csvColumns {
		head = append(head, c.name)
	}
	cw.Write(head)
	for _, g := range r.Groups {
		for _, d := range []struct {
			doer string
			m    *Metrics
		}{{"agent", &g.Agent}, {"person", &g.Person}} {
			line := []string{r.Key, r.By, g.Name, d.doer}
			for _, c := range csvColumns {
				line = append(line, c.val(d.m))
			}
			cw.Write(line)
		}
	}
	cw.Flush()
	return cw.Error()
}
