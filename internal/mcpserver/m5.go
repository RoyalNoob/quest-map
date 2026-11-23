package mcpserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/suite"
)

// Suites returns the board's suites (reloaded when a suite file changes).
func (s *Service) Suites() *suite.Live { return s.suites }

// suiteFor returns a project's suite. Problems with suite files do not stop work (suite.Live.For); they are
// reported on the reply's note line, not as errors, so agents keep working.
func (s *Service) suiteFor(p *board.Project) (*suite.Suite, string) {
	st, err := s.suites.For(s.b, p)
	if err != nil {
		return st, "Note: suite problem: " + err.Error()
	}
	return st, ""
}

// fieldKeys returns the custom field keys of a project's suite.
func (s *Service) fieldKeys(p *board.Project) []string {
	if st, _ := s.suiteFor(p); st != nil {
		return st.FieldKeys()
	}
	return nil
}

// checkFields refuses custom fields the project's suite does not declare (a typo would otherwise add a key
// nobody sees).
func (s *Service) checkFields(p *board.Project, in map[string]string) error {
	return s.CheckFields(p, in)
}

// CheckFields refuses custom fields the project's suite does not declare. Clearing one ("" value) is always
// allowed, so a value left from a field the suite has since dropped can be removed.
func (s *Service) CheckFields(p *board.Project, in map[string]string) error {
	keys := s.fieldKeys(p)
	for k, v := range in {
		if strings.TrimSpace(v) == "" && board.IsCustomKey(k) {
			continue
		}
		if !slices.Contains(keys, k) {
			if len(keys) == 0 {
				return errorf("this project's suite has no custom fields (fields: %q)", k)
			}
			return errorf("no field %q in this project's suite; fields: %s", k, strings.Join(keys, ", "))
		}
	}
	return nil
}

// suiteNotes warns about values a task has that the project's suite does not list. Such values stay valid
// (spec §4.1); the note helps agents pick listed ones.
func (s *Service) suiteNotes(p *board.Project, t *board.Task) string {
	st, problem := s.suiteFor(p)
	var notes []string
	if problem != "" {
		notes = append(notes, problem)
	}
	if st == nil {
		return join(notes...)
	}
	if t.Discipline != "" && len(st.Disciplines) > 0 && !st.HasDiscipline(t.Discipline) {
		notes = append(notes, fmt.Sprintf("Note: %s %q is not in this project's list (%s).", st.Words.Discipline, t.Discipline, strings.Join(st.Disciplines, ", ")))
	}
	if t.Milestone != "" {
		known := false
		for _, m := range p.Config().Milestones {
			known = known || strings.EqualFold(m.Name, t.Milestone)
		}
		if !known {
			notes = append(notes, fmt.Sprintf("Note: milestone %q is not defined in %s.", t.Milestone, p.Slug()))
		}
	}
	for _, f := range st.Fields {
		if v := t.Custom[f.Key]; v != "" && f.Type == "choice" && !slices.ContainsFunc(f.Values, func(x string) bool { return strings.EqualFold(x, v) }) {
			notes = append(notes, fmt.Sprintf("Note: %s %q is not in this project's list (%s).", f.Label, v, strings.Join(f.Values, ", ")))
		}
	}
	rels, _ := p.Releases()
	for _, f := range []struct{ what, id string }{{"found in", t.FoundIn}, {"fixed in", t.FixedIn}} {
		if f.id != "" && !slices.ContainsFunc(rels, func(r board.Release) bool { return strings.EqualFold(r.ID, f.id) }) {
			notes = append(notes, fmt.Sprintf("Note: %s %s %q is not recorded yet (release_record).", f.what, st.Words.Release, f.id))
		}
	}
	return join(notes...)
}

// pipelineStart validates a pipeline name against the project's suite and returns it at its first step.
func (s *Service) pipelineStart(p *board.Project, name string) (*board.Pipeline, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return &board.Pipeline{}, nil // clears
	}
	st, _ := s.suiteFor(p)
	pl, ok := st.Pipeline(name)
	if !ok {
		var names []string
		for _, x := range st.Pipelines {
			names = append(names, x.Name)
		}
		return nil, errorf("no pipeline %q in this project's suite; pipelines: %s", name, strings.Join(names, ", "))
	}
	return &board.Pipeline{Name: pl.Name, At: pl.Steps[0]}, nil
}

// ---- pipeline_advance ----

// AdvanceIn is the input of pipeline_advance.
type AdvanceIn struct {
	ID   string `json:"id" jsonschema:"task id, e.g. RPG-12"`
	To   string `json:"to,omitempty" jsonschema:"step to move to; default: the next step"`
	Note string `json:"note,omitempty" jsonschema:"what was done in the step just finished"`
	UsageArgs
}

// Advance moves an item task to the next step of its pipeline (or to a named step).
func (s *Service) Advance(c Caller, in AdvanceIn) (string, TaskReply, error) {
	p, id, err := s.projectOfID(in.ID)
	if err != nil {
		return "", TaskReply{}, err
	}
	cur, err := p.Get(id)
	if err != nil {
		return "", TaskReply{}, err
	}
	st, _ := s.suiteFor(p)
	pl, next, err := s.NextStep(p, cur, in.To)
	if err != nil {
		return "", TaskReply{}, err
	}
	stale, err := s.staleClaim(c, p, cur)
	if err != nil {
		return "", TaskReply{}, err
	}
	from := cur.Pipeline.At
	logLine := fmt.Sprintf("%s %s: %s → %s", st.Words.Item, pl.Name, from, next)
	if n := strings.TrimSpace(in.Note); n != "" {
		logLine += ". " + n
	}
	e := board.Edit{Pipeline: &board.Pipeline{Name: pl.Name, At: next}}
	text, out, err := s.write(c, p, id, in.UsageArgs, agentCheck(c, false, stale), func(by board.Actor, u *board.Usage, check func(*board.Task) error) (*board.Task, error) {
		return p.UpdateIf(id, by, e, logLine, u, func(t *board.Task) error {
			if err := check(t); err != nil {
				return err
			}
			if t.Pipeline == nil || t.Pipeline.At != from {
				return errorf("%s's pipeline step changed meanwhile; read it again (task_show)", id)
			}
			return nil
		})
	})
	if err != nil {
		return "", TaskReply{}, err
	}
	pos := slices.Index(pl.Steps, next) + 1
	return join(fmt.Sprintf("%s %s: %s → %s (step %d of %d)", id, pl.Name, from, next, pos, len(pl.Steps)), text), out, nil
}

// NextStep works out a pipeline move for a task: its pipeline (from the project's suite) and the step to move
// to – the named one, or the one after the current step.
func (s *Service) NextStep(p *board.Project, t *board.Task, to string) (suite.Pipeline, string, error) {
	st, _ := s.suiteFor(p)
	if t.Pipeline == nil || t.Pipeline.Name == "" {
		return suite.Pipeline{}, "", errorf("%s has no pipeline; set one first (task_edit pipeline)", t.ID)
	}
	pl, ok := st.Pipeline(t.Pipeline.Name)
	if !ok {
		return pl, "", errorf("%s follows pipeline %q, which this project's suite does not define", t.ID, t.Pipeline.Name)
	}
	if to = strings.TrimSpace(to); to != "" {
		j := slices.IndexFunc(pl.Steps, func(x string) bool { return strings.EqualFold(x, to) })
		if j < 0 {
			return pl, "", errorf("pipeline %s has no step %q; steps: %s", pl.Name, to, strings.Join(pl.Steps, " → "))
		}
		return pl, pl.Steps[j], nil
	}
	i := slices.Index(pl.Steps, t.Pipeline.At)
	if i == len(pl.Steps)-1 {
		return pl, "", errorf("%s is already at the last step of %s (%s); move the task to review when it is done", t.ID, pl.Name, t.Pipeline.At)
	}
	return pl, pl.Steps[i+1], nil // i == -1 (unknown step) starts at the first
}

// ---- release_record ----

// RecordReleaseIn is the input of release_record.
type RecordReleaseIn struct {
	Project  string `json:"project,omitempty" jsonschema:"project folder or key; default: the project linked to your working folder"`
	ID       string `json:"id" jsonschema:"release id (game dev: build id such as b2026-11-19.1)"`
	Target   string `json:"target,omitempty" jsonschema:"what it is for (game dev: platform such as Win64)"`
	Revision string `json:"revision,omitempty" jsonschema:"VCS revision it was made from"`
	Notes    string `json:"notes,omitempty"`
}

// ReleaseOut is a recorded release.
type ReleaseOut struct {
	Release board.Release `json:"release"`
	Project string        `json:"project"`
}

// RecordRelease stores a release in the project's releases.toml.
func (s *Service) RecordRelease(c Caller, in RecordReleaseIn) (string, ReleaseOut, error) {
	p, err := s.project(c, in.Project)
	if err != nil {
		return "", ReleaseOut{}, err
	}
	r, err := p.RecordRelease(board.Release{ID: in.ID, Target: in.Target, Revision: in.Revision, Notes: in.Notes})
	if err != nil {
		return "", ReleaseOut{}, err
	}
	s.ix.RefreshReleases(p.Slug())
	st, problem := s.suiteFor(p)
	text := fmt.Sprintf("Recorded %s %s", st.Words.Release, r.ID)
	if r.Target != "" {
		text += " (" + st.Words.Target + " " + r.Target + ")"
	}
	text += " in " + p.Slug() + "."
	if r.Target != "" && len(st.ReleaseTargets) > 0 && !slices.ContainsFunc(st.ReleaseTargets, func(x string) bool { return strings.EqualFold(x, r.Target) }) {
		text += fmt.Sprintf(" Note: %s is not in the usual %ss (%s).", r.Target, st.Words.Target, strings.Join(st.ReleaseTargets, ", "))
	}
	return join(text, problem), ReleaseOut{Release: r, Project: p.Slug()}, nil
}

// ---- feedback ----

// FeedbackIn is a feedback report (game dev: a playtest note).
type FeedbackIn struct {
	Project        string `json:"project,omitempty" jsonschema:"project folder or key; default: the project linked to your working folder"`
	Text           string `json:"text" jsonschema:"what happened; the first line becomes the title"`
	Release        string `json:"release,omitempty" jsonschema:"release id it was found in (game dev: build id)"`
	Reporter       string `json:"reporter,omitempty" jsonschema:"who reported it"`
	ScreenshotB64  string `json:"screenshot_base64,omitempty" jsonschema:"optional image, base64-encoded, up to 10 MB"`
	ScreenshotName string `json:"screenshot_name,omitempty"`
	// Via names the channel for REST callers (the CLI sets "the CLI"); tools and the listener set their own.
	Via string `json:"via,omitempty" jsonschema:"leave empty"`
}

// FileFeedback turns a feedback report into a task with the project's suite intake defaults. The UI, the
// CLI, the intake listener and the feedback_report tool all use it. via names the channel for the body. note
// is a warning for the reporter ("" if none): a release id the project has not recorded is filed as given, but
// said so, since it is often a typo. An error with a non-nil task means the task was filed anyway.
func (s *Service) FileFeedback(p *board.Project, in FeedbackIn, by board.Actor, via string) (t *board.Task, note string, err error) {
	text := strings.TrimSpace(strings.ReplaceAll(in.Text, "\r\n", "\n"))
	if text == "" {
		return nil, "", errors.New("the report is empty")
	}
	var shot []byte
	if in.ScreenshotB64 != "" {
		if shot, err = base64.StdEncoding.DecodeString(in.ScreenshotB64); err != nil {
			return nil, "", errors.New("screenshot_base64 is not valid base64")
		}
		if len(shot) > board.MaxAttachment {
			return nil, "", fmt.Errorf("the screenshot is larger than %d MB", board.MaxAttachment>>20)
		}
	}
	st, _ := s.suiteFor(p)
	// The first line is the title. The body keeps the rest, and the whole first line only when the title had
	// to be shortened.
	title, rest, _ := strings.Cut(text, "\n")
	body := strings.TrimSpace(rest)
	if r := []rune(title); len(r) > 80 {
		title = string(r[:79]) + "…"
		body = text
	}
	who := board.CleanName(in.Reporter, 40)
	if who == "" {
		who = "someone"
	}
	footer := fmt.Sprintf("%s from %s via %s, %s.", capitalize(st.Words.Feedback), who, via, s.b.Now().In(s.b.Location()).Format("2006-01-02 15:04 MST"))
	release := strings.TrimSpace(in.Release)
	if release != "" && !s.releaseKnown(p, release) {
		note = fmt.Sprintf("%s %s is not recorded in %s; the report was filed with it anyway – check the id, or record it with release_record.",
			capitalize(st.Words.Release), release, p.Slug())
		footer += fmt.Sprintf(" %s %s was not recorded when this was filed.", capitalize(st.Words.Release), release)
	}
	t = &board.Task{Title: title, Body: strings.TrimSpace(body + "\n\n" + footer), Status: board.Status(st.Intake.Status),
		Doer: board.Doer(st.Intake.Doer), Priority: board.Priority(st.Intake.Priority), Discipline: st.Intake.Discipline,
		Labels: slices.Clone(st.Intake.Labels), FoundIn: release}
	if t, err = p.Create(t, by, nil); err != nil {
		return nil, "", err
	}
	if len(shot) > 0 {
		name := in.ScreenshotName
		if name == "" {
			name = "screenshot.png"
		}
		saved, err := p.SaveAttachment(t.ID, name, shot)
		if err != nil {
			return t, note, fmt.Errorf("%s was filed, but the screenshot was not saved: %w", t.ID, err)
		}
		link := fmt.Sprintf("![%s](../attachments/%s/%s)", saved, t.ID, saved)
		newBody := t.Body + "\n\n" + link
		if _, err := p.Update(t.ID, by, board.Edit{Body: &newBody}, "", nil); err != nil {
			return t, note, err
		}
		t.Body = newBody
	}
	s.refresh(p, t)
	return t, note, nil
}

// releaseKnown reports whether a project has recorded a release with this id. If releases.toml cannot be read
// it says yes, so a broken file does not put a warning on every report.
func (s *Service) releaseKnown(p *board.Project, id string) bool {
	rs, err := p.Releases()
	if err != nil {
		return true
	}
	return slices.ContainsFunc(rs, func(r board.Release) bool { return strings.EqualFold(r.ID, id) })
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// Feedback is the feedback_report tool.
func (s *Service) Feedback(c Caller, in FeedbackIn) (string, TaskReply, error) {
	p, err := s.project(c, in.Project)
	if err != nil {
		return "", TaskReply{}, err
	}
	t, note, err := s.FileFeedback(p, in, s.actor(c), "agent "+c.ID)
	if err != nil {
		if t == nil {
			return "", TaskReply{}, err
		}
		note = join(note, err.Error()) // filed; say what went wrong
	}
	if note != "" {
		note = "Note: " + note
	}
	st, _ := s.suiteFor(p)
	o := taskOut(p, t, nil)
	text := fmt.Sprintf("Filed %s as %s", st.Words.Feedback, line(o))
	if t.FoundIn != "" {
		text += fmt.Sprintf(" – found in %s %s", st.Words.Release, t.FoundIn)
	}
	return join(text, note, s.suiteNotes(p, t)), TaskReply{Task: o}, nil
}
