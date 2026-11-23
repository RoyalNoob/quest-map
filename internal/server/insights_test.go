package server

import (
	"testing"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/insights"
)

func TestInsightsAndActivityAPI(t *testing.T) {
	s, p := startWithProject(t)
	agent := board.Actor{Kind: "agent", Session: "s1", Model: "m"}
	if _, err := p.Claim("RPG-1", agent); err != nil {
		t.Fatal(err)
	}
	review := board.Review
	if _, err := p.Update("RPG-1", agent, board.Edit{Status: &review}, "ready", nil); err != nil {
		t.Fatal(err)
	}
	br := newBrowser(t, s)

	var rep insights.Report
	if code := br.do("GET", "/api/insights?project=rpg&by=discipline", nil, &rep); code != 200 {
		t.Fatalf("insights = %d", code)
	}
	if len(rep.Groups) != 1 || rep.Groups[0].Name != "code" || rep.Groups[0].Agent.Reviewed != 1 || rep.Groups[0].Agent.Claims != 1 {
		t.Errorf("insights = %+v", rep.Groups)
	}
	if code := br.do("GET", "/api/insights", nil, nil); code != 400 {
		t.Errorf("no project with two projects: %d, want 400", code)
	}
	if code := br.do("GET", "/api/insights?project=rpg&by=colour", nil, nil); code != 400 {
		t.Errorf("bad by: %d, want 400", code)
	}

	var recs []activityJSON
	if code := br.do("GET", "/api/activity?project=rpg", nil, &recs); code != 200 {
		t.Fatalf("activity = %d", code)
	}
	statuses := 0
	for _, r := range recs {
		if r.Project != "rpg" || r.ID == "" {
			t.Errorf("record %+v", r)
		}
		if r.Kind == board.KindStatus {
			statuses++
		}
	}
	if statuses != 3 { // created, claimed, review
		t.Errorf("status records = %d, want 3", statuses)
	}
	if code := br.do("GET", "/api/activity?since=later", nil, nil); code != 400 {
		t.Errorf("bad since: %d, want 400", code)
	}
}
