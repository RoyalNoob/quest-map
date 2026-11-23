// Package insights computes the spec §8.4 metrics – how well the agent setup works – from a project's
// activity records and task fields.
package insights

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// Price is a model's rate in USD per million tokens, by token type (board.toml [prices.<model>]).
type Price struct {
	Input         float64 `toml:"input" json:"input"`
	Output        float64 `toml:"output" json:"output"`
	CacheRead     float64 `toml:"cacheRead" json:"cacheRead"`
	CacheCreation float64 `toml:"cacheCreation" json:"cacheCreation"`
}

// Prices maps a model name to its price.
type Prices map[string]Price

// LoadPrices reads [prices.<model>] from root/board.toml. A missing file or table means no prices.
func LoadPrices(root string) (Prices, error) {
	data, err := os.ReadFile(filepath.Join(root, "board.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return Prices{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Prices Prices `toml:"prices"`
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("board.toml: %w", err)
	}
	if f.Prices == nil {
		f.Prices = Prices{}
	}
	return f.Prices, nil
}

// cost prices a usage record: its own cost_usd when the measured source gave one, else its tokens at the
// model's price. ok is false when tokens could not be priced (unknown model, or only a total).
func (ps Prices) cost(r board.Record) (usd float64, ok bool) {
	if r.CostUSD != nil {
		return *r.CostUSD, true
	}
	if r.Tokens == nil || r.Tokens.IsZero() {
		return 0, true
	}
	p, known := ps[r.Model]
	t := r.Tokens
	usd = (float64(t.Input)*p.Input + float64(t.Output)*p.Output + float64(t.CacheRead)*p.CacheRead +
		float64(t.CacheCreation)*p.CacheCreation) / 1e6
	return usd, known && t.Total == 0
}

// Group-by dimensions.
var Dimensions = []string{"model", "client", "discipline", "label", "milestone"}

// Options choose what a report covers.
type Options struct {
	Since    time.Time     // tasks with activity at or after this time; zero: all
	Now      time.Time     // zero: time.Now
	By       string        // "" or one of Dimensions
	Task     string        // only this task
	ClaimTTL time.Duration // a doing task this long without activity counts as an abandoned claim; 0: never
}

// FewTasks is the sample size under which a group is shown greyed out.
const FewTasks = 5

// Report is the insights of one project.
type Report struct {
	Project  string    `json:"project"`
	Key      string    `json:"key"`
	Since    time.Time `json:"since,omitzero"`
	Until    time.Time `json:"until"`
	By       string    `json:"by"`
	Groups   []Group   `json:"groups"`
	Overhead float64   `json:"overhead_usd"` // usage with no task, in the period
	// Unpriced lists models whose tokens had no price (cost counted as 0); "(total only)" for self-reports
	// that gave only a total.
	Unpriced []string `json:"unpriced"`
	// Queue is the agent review queue by day: tasks waiting in review at the end of the day, moves to review
	// and moves to done that day.
	Queue []QueueDay `json:"queue"`
}

// Group holds the metrics of one group value, agent tasks next to person tasks (the baseline).
type Group struct {
	Name   string  `json:"name"` // "" when not grouped; "(none)" for tasks without a value
	Agent  Metrics `json:"agent"`
	Person Metrics `json:"person"`
}

// QueueDay is one day of the agent review queue.
type QueueDay struct {
	Day      string `json:"day"` // YYYY-MM-DD in the board time zone
	Depth    int    `json:"depth"`
	ToReview int    `json:"to_review"`
	ToDone   int    `json:"to_done"`
}

// Metrics are the §8.4 figures over a set of tasks. Counts are the samples; rates are nil when their sample
// is empty.
type Metrics struct {
	Tasks      int `json:"tasks"`
	MultiModel int `json:"multi_model"` // tasks that used several models

	// First-pass acceptance: of the tasks whose first review was decided, those that went to done.
	Decided       int      `json:"decided"`
	FirstPass     int      `json:"first_pass"`
	FirstPassRate *float64 `json:"first_pass_rate"`

	// Rework: moves back from review, and what was spent after the first review.
	Reviewed     int     `json:"reviewed"`
	ReworkMoves  int     `json:"rework_moves"`
	ReworkTokens int64   `json:"rework_tokens"`
	ReworkTimeS  int64   `json:"rework_time_s"`
	ReworkCost   float64 `json:"rework_usd"`

	// Reopened after done.
	ReachedDone int      `json:"reached_done"`
	Reopened    int      `json:"reopened"`
	Regressions int      `json:"regressions"`
	ReopenRate  *float64 `json:"reopen_rate"`

	// Cost.
	Done            int      `json:"done"`
	DoneCost        float64  `json:"done_usd"`
	CostPerAccepted *float64 `json:"cost_per_accepted"`
	WasteTasks      int      `json:"waste_tasks"` // not done, with an abandoned claim
	Waste           float64  `json:"waste_usd"`
	Cost            float64  `json:"cost_usd"`
	Tokens          int64    `json:"tokens"`

	// Abandonment.
	Claims      int      `json:"claims"`
	Abandoned   int      `json:"abandoned"`
	AbandonRate *float64 `json:"abandon_rate"`

	// Human attention.
	ReviewWaitN    int      `json:"review_wait_n"`
	ReviewWaitMedS int64    `json:"review_wait_median_s"`
	PersonNotes    int      `json:"person_notes"`
	NotesPerTask   *float64 `json:"notes_per_task"`

	// Cycle time (medians).
	ClaimToReviewN    int   `json:"claim_to_review_n"`
	ClaimToReviewMedS int64 `json:"claim_to_review_median_s"`
	ReviewToDoneN     int   `json:"review_to_done_n"`
	ReviewToDoneMedS  int64 `json:"review_to_done_median_s"`

	// Coverage: worked tasks with usage records; measured vs self-reported tokens.
	Worked         int      `json:"worked"`
	WithUsage      int      `json:"with_usage"`
	Coverage       *float64 `json:"coverage"`
	MeasuredTokens int64    `json:"measured_tokens"`
	SelfTokens     int64    `json:"self_tokens"`
	MeasuredShare  *float64 `json:"measured_share"`

	reviewWait, claimToReview, reviewToDone []time.Duration
}

// taskStats are the facts of one task, from its records.
type taskStats struct {
	t                 *board.Task
	model, client     string
	multiModel        bool
	cost              float64
	tokens            int64
	measured, self    int64
	usageRecs         int
	reviewed, decided bool
	firstPass         bool
	reworkMoves       int
	reworkTok         int64
	reworkTime        time.Duration
	reworkCost        float64
	reachedDone       bool
	reopened          bool
	claims, abandoned int
	reviewWait        time.Duration
	personNotes       int
	claimToReview     time.Duration // -1: none
	reviewToDone      time.Duration // -1: none
}

// Query holds the arguments every surface (CLI, MCP, HTTP) takes, as text.
type Query struct {
	Since string `json:"since,omitempty" jsonschema:"period start: a date like 2026-11-01 or an age like 30d, 2w; default all time"`
	By    string `json:"by,omitempty" jsonschema:"group by model, client, discipline, label or milestone"`
	Task  string `json:"task,omitempty" jsonschema:"only this task id"`
}

// Build reads board.toml prices and computes p's report for q.
func Build(b *board.Board, p *board.Project, q Query, claimTTL time.Duration) (*Report, error) {
	prices, err := LoadPrices(b.Root())
	if err != nil {
		return nil, err
	}
	now := b.Now()
	since, err := ParseSince(q.Since, now, b.Location())
	if err != nil {
		return nil, err
	}
	return Compute(b, p, prices, Options{Since: since, Now: now, By: strings.ToLower(strings.TrimSpace(q.By)),
		Task: strings.TrimSpace(q.Task), ClaimTTL: claimTTL})
}

// Compute builds the report of project p.
func Compute(b *board.Board, p *board.Project, prices Prices, opt Options) (*Report, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	if opt.By != "" && !slices.Contains(Dimensions, opt.By) {
		return nil, fmt.Errorf("by %q: use one of %s", opt.By, strings.Join(Dimensions, ", "))
	}
	tasks, _ := p.List()
	archived, _ := p.ListArchived()
	byID := map[string]*board.Task{}
	for _, t := range append(tasks, archived...) {
		byID[t.ID] = t
	}
	l, err := b.Ledger()
	if err != nil {
		return nil, err
	}
	owned := map[string][]board.Record{}
	for _, recs := range l.Records {
		for _, r := range recs {
			if o := l.Owner(r); byID[o] != nil {
				owned[o] = append(owned[o], r)
			}
		}
	}
	releases, err := p.Releases()
	if err != nil {
		return nil, err
	}
	relDate := map[string]time.Time{}
	for _, r := range releases {
		relDate[r.ID] = r.Date
	}

	rep := &Report{Project: p.Slug(), Key: p.Key(), Since: opt.Since, Until: opt.Now.UTC(), By: opt.By}
	unpriced := map[string]bool{}
	for _, r := range l.Records[p.Slug()] {
		if opt.Task == "" && r.Kind == board.KindUsage && r.TaskID() == "" && l.Counts(r) && !r.At.Before(opt.Since) {
			c, ok := prices.cost(r)
			rep.Overhead += c
			noteUnpriced(unpriced, r, ok)
		}
	}

	groups := map[string]*Group{}
	var agentRecs [][]board.Record
	ids := make([]string, 0, len(owned))
	for id := range owned {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if opt.Task != "" && !strings.EqualFold(id, opt.Task) {
			continue
		}
		recs := owned[id]
		sort.SliceStable(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
		// A task waiting in review or doing since before the period is still open work in it.
		if t := byID[id]; recs[len(recs)-1].At.Before(opt.Since) && t.Status != board.Review && t.Status != board.Doing {
			continue
		}
		st := analyze(byID[id], recs, l, prices, unpriced, opt)
		regression := false
		if st.reopened && st.t.FoundIn != "" && st.t.FixedIn != "" {
			f, fok := relDate[st.t.FoundIn]
			x, xok := relDate[st.t.FixedIn]
			regression = fok && xok && !f.Before(x)
		}
		for _, name := range groupNames(st, opt.By) {
			g := groups[name]
			if g == nil {
				g = &Group{Name: name}
				groups[name] = g
			}
			m := &g.Person
			if st.t.Doer == board.Agent {
				m = &g.Agent
			}
			m.add(st, regression)
		}
		if st.t.Doer == board.Agent {
			agentRecs = append(agentRecs, recs)
		}
	}
	for _, g := range groups {
		g.Agent.finish()
		g.Person.finish()
		rep.Groups = append(rep.Groups, *g)
	}
	sort.Slice(rep.Groups, func(i, j int) bool {
		a, b := rep.Groups[i], rep.Groups[j]
		if na, nb := a.Agent.Tasks+a.Person.Tasks, b.Agent.Tasks+b.Person.Tasks; na != nb {
			return na > nb
		}
		return a.Name < b.Name
	})
	for m := range unpriced {
		rep.Unpriced = append(rep.Unpriced, m)
	}
	sort.Strings(rep.Unpriced)
	rep.Queue = queue(agentRecs, opt, b.Location())
	return rep, nil
}

func noteUnpriced(set map[string]bool, r board.Record, ok bool) {
	if ok {
		return
	}
	switch {
	case r.Tokens.Total != 0:
		set["(total only)"] = true
	case r.Model == "":
		set["(no model)"] = true
	default:
		set[r.Model] = true
	}
}

// groupNames returns the groups a task belongs to: one, or one per label.
func groupNames(st *taskStats, by string) []string {
	v := ""
	switch by {
	case "":
		return []string{""}
	case "model":
		v = st.model
	case "client":
		v = st.client
	case "discipline":
		v = st.t.Discipline
	case "milestone":
		v = st.t.Milestone
	case "label":
		if len(st.t.Labels) > 0 {
			return st.t.Labels
		}
	}
	if v == "" {
		v = "(none)"
	}
	return []string{v}
}

// analyze walks one task's records in time order.
func analyze(t *board.Task, recs []board.Record, l *board.Ledger, prices Prices, unpriced map[string]bool, opt Options) *taskStats {
	st := &taskStats{t: t, claimToReview: -1, reviewToDone: -1}
	var firstClaim, firstReview, inReview time.Time
	holder := ""
	modelTok, clientTok := map[string]int64{}, map[string]int64{}
	claimModel, claimClient := "", ""
	// statusAt reports whether a record of kind shares recs[i]'s write (same task, same time; neighbours in
	// time order).
	statusAt := func(i int, kind string, match func(board.Record) bool) bool {
		for _, step := range []int{-1, 1} {
			for j := i + step; j >= 0 && j < len(recs) && recs[j].At.Equal(recs[i].At); j += step {
				if recs[j].Kind == kind && match(recs[j]) {
					return true
				}
			}
		}
		return false
	}
	for i, r := range recs {
		switch r.Kind {
		case board.KindUsage:
			if !l.Counts(r) {
				continue
			}
			c, ok := prices.cost(r)
			noteUnpriced(unpriced, r, ok)
			n := int64(0)
			if r.Tokens != nil {
				n = r.Tokens.Sum()
			}
			st.usageRecs++
			st.cost += c
			st.tokens += n
			switch r.Source {
			case board.SourceMeasured:
				st.measured += n
			case board.SourceSelf:
				st.self += n
			}
			if n > 0 && r.Model != "" {
				modelTok[r.Model] += n
			}
			if n > 0 && r.Client != "" {
				clientTok[clientName(r.Client)] += n
			}
			if !firstReview.IsZero() && r.At.After(firstReview) {
				st.reworkTok += n
				st.reworkTime += time.Duration(r.TimeS) * time.Second
				st.reworkCost += c
			}
		case board.KindStatus:
			from, to := board.Status(r.From), board.Status(r.To)
			if to == board.Doing {
				st.claims++
				holder = r.Session
				if firstClaim.IsZero() {
					firstClaim = r.At
					claimModel, claimClient = r.Model, clientName(r.Client)
				}
			}
			if from == board.Doing {
				holder = ""
				if to == board.Todo || to == board.Backlog {
					st.abandoned++ // released
				}
			}
			if from == board.Review && !inReview.IsZero() {
				st.reviewWait += r.At.Sub(inReview)
				inReview = time.Time{}
			}
			if from == board.Review && !firstReview.IsZero() {
				if !st.decided {
					st.decided, st.firstPass = true, to == board.Done
				}
				if to == board.Doing || to == board.Todo || to == board.Backlog {
					st.reworkMoves++
				}
			}
			if to == board.Review {
				if firstReview.IsZero() {
					firstReview = r.At
					st.reviewed = true
					if !firstClaim.IsZero() {
						st.claimToReview = r.At.Sub(firstClaim)
					}
				}
				inReview = r.At
				if statusAt(i, board.KindNote, func(n board.Record) bool {
					return strings.Contains(strings.ToLower(n.Text), board.CannotContinue)
				}) {
					st.abandoned++
				}
			}
			if to == board.Done {
				st.reachedDone = true
				if !firstReview.IsZero() && st.reviewToDone < 0 {
					st.reviewToDone = r.At.Sub(firstReview)
				}
			}
			if from == board.Done && st.reachedDone {
				st.reopened = true
			}
		case board.KindEdit:
			// A claim taken over without a status change: another agent took a stale claim.
			if r.Actor != "agent" || r.Session == "" || !slices.Contains(r.Fields, "claim") ||
				statusAt(i, board.KindStatus, func(board.Record) bool { return true }) || r.Session == holder {
				continue
			}
			if holder != "" {
				st.abandoned++
			}
			st.claims++
			holder = r.Session
		case board.KindNote:
			if r.Actor == "person" {
				st.personNotes++
			}
		}
	}
	if !inReview.IsZero() {
		st.reviewWait += opt.Now.Sub(inReview)
	}
	if opt.ClaimTTL > 0 && t.Status == board.Doing && t.Claim != nil && opt.Now.Sub(recs[len(recs)-1].At) > opt.ClaimTTL {
		st.abandoned++ // the current claim went stale
	}
	st.model, st.multiModel = top(modelTok), len(modelTok) > 1
	if st.model == "" {
		st.model = claimModel
	}
	if st.client = top(clientTok); st.client == "" {
		st.client = claimClient
	}
	return st
}

// clientName drops the version from "claude-code/2.4.1".
func clientName(c string) string {
	name, _, _ := strings.Cut(c, "/")
	return name
}

// top returns the key with the largest count; ties go to the first name.
func top(m map[string]int64) string {
	best := ""
	for k, v := range m {
		if best == "" || v > m[best] || (v == m[best] && k < best) {
			best = k
		}
	}
	return best
}

func (m *Metrics) add(st *taskStats, regression bool) {
	m.Tasks++
	if st.multiModel {
		m.MultiModel++
	}
	if st.decided {
		m.Decided++
		if st.firstPass {
			m.FirstPass++
		}
	}
	if st.reviewed {
		m.Reviewed++
		m.ReviewWaitN++
		m.reviewWait = append(m.reviewWait, st.reviewWait)
	}
	m.ReworkMoves += st.reworkMoves
	m.ReworkTokens += st.reworkTok
	m.ReworkTimeS += int64(st.reworkTime / time.Second)
	m.ReworkCost += st.reworkCost
	if st.reachedDone {
		m.ReachedDone++
	}
	if st.reopened {
		m.Reopened++
	}
	if regression {
		m.Regressions++
	}
	if st.t.Status == board.Done {
		m.Done++
		m.DoneCost += st.cost
	} else if st.abandoned > 0 {
		m.WasteTasks++
		m.Waste += st.cost
	}
	m.Cost += st.cost
	m.Tokens += st.tokens
	m.Claims += st.claims
	m.Abandoned += st.abandoned
	m.PersonNotes += st.personNotes
	if st.claimToReview >= 0 {
		m.claimToReview = append(m.claimToReview, st.claimToReview)
	}
	if st.reviewToDone >= 0 {
		m.reviewToDone = append(m.reviewToDone, st.reviewToDone)
	}
	if st.claims > 0 || st.usageRecs > 0 {
		m.Worked++
		if st.usageRecs > 0 {
			m.WithUsage++
		}
	}
	m.MeasuredTokens += st.measured
	m.SelfTokens += st.self
}

func ratio(a, b float64) *float64 {
	if b == 0 {
		return nil
	}
	r := a / b
	return &r
}

// median returns the median in whole seconds (the lower middle for an even count).
func median(ds []time.Duration) int64 {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return int64(s[(len(s)-1)/2] / time.Second)
}

func (m *Metrics) finish() {
	m.FirstPassRate = ratio(float64(m.FirstPass), float64(m.Decided))
	m.ReopenRate = ratio(float64(m.Reopened), float64(m.ReachedDone))
	m.CostPerAccepted = ratio(m.DoneCost, float64(m.Done))
	m.AbandonRate = ratio(float64(m.Abandoned), float64(m.Claims))
	m.NotesPerTask = ratio(float64(m.PersonNotes), float64(m.Tasks))
	m.Coverage = ratio(float64(m.WithUsage), float64(m.Worked))
	m.MeasuredShare = ratio(float64(m.MeasuredTokens), float64(m.MeasuredTokens+m.SelfTokens))
	m.ReviewWaitMedS = median(m.reviewWait)
	m.ClaimToReviewN, m.ClaimToReviewMedS = len(m.claimToReview), median(m.claimToReview)
	m.ReviewToDoneN, m.ReviewToDoneMedS = len(m.reviewToDone), median(m.reviewToDone)
}

// maxQueueDays bounds the queue series.
const maxQueueDays = 92

// queue counts the agent review queue per day, from the period start (or the first record) to now.
func queue(taskRecs [][]board.Record, opt Options, loc *time.Location) []QueueDay {
	if len(taskRecs) == 0 {
		return []QueueDay{}
	}
	start := opt.Since
	if start.IsZero() {
		start = opt.Now
		for _, recs := range taskRecs {
			if recs[0].At.Before(start) {
				start = recs[0].At
			}
		}
	}
	day := func(t time.Time) time.Time {
		y, m, d := t.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	}
	first, last := day(start), day(opt.Now)
	if earliest := last.AddDate(0, 0, -(maxQueueDays - 1)); first.Before(earliest) { // calendar days, DST-safe
		first = earliest
	}
	var out []QueueDay
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		end := d.AddDate(0, 0, 1)
		q := QueueDay{Day: d.Format("2006-01-02")}
		for _, recs := range taskRecs {
			inReview := false
			for _, r := range recs {
				if r.Kind != board.KindStatus || !r.At.Before(end) {
					continue
				}
				inReview = r.To == string(board.Review)
				if !r.At.Before(d) {
					switch board.Status(r.To) {
					case board.Review:
						q.ToReview++
					case board.Done:
						q.ToDone++
					}
				}
			}
			if inReview {
				q.Depth++
			}
		}
		out = append(out, q)
	}
	return out
}

// ParseSince reads a period start: a date (2026-11-01), or an age such as 30d, 2w or 12h before now. "" means
// no limit.
func ParseSince(s string, now time.Time, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t, nil
	}
	if days := map[byte]int{'d': 1, 'w': 7}[s[len(s)-1]]; days > 0 {
		if n, err := strconv.Atoi(s[:len(s)-1]); err == nil && n >= 0 {
			return now.AddDate(0, 0, -days*n), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("since %q: use a date like 2026-11-01 or an age like 30d, 2w or 12h", s)
}
