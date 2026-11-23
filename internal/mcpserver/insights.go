package mcpserver

import (
	"strings"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/insights"
)

// InsightsIn is the input of the insights tool.
type InsightsIn struct {
	Project string `json:"project,omitempty" jsonschema:"project folder or key; default: the project linked to your working folder"`
	insights.Query
}

// Insights reports the §8.4 metrics of a project, or of one task.
func (s *Service) Insights(c Caller, in InsightsIn) (string, insights.Report, error) {
	var p *board.Project
	var err error
	if in.Project == "" && in.Task != "" {
		p, _, err = s.projectOfID(in.Task)
	} else {
		p, err = s.project(c, in.Project)
	}
	if err != nil {
		return "", insights.Report{}, err
	}
	rep, err := insights.Build(s.b, p, in.Query, s.ix.ClaimTTL())
	if err != nil {
		return "", insights.Report{}, err
	}
	var b strings.Builder
	if err := rep.WriteText(&b); err != nil {
		return "", insights.Report{}, err
	}
	return strings.TrimRight(b.String(), "\n"), *rep, nil
}
