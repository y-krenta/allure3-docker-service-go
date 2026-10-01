package e2e

import (
	"net/http"
	"slices"
	"testing"
)

func TestClearHistoryStartsOver(t *testing.T) {
	c := newClient(t)
	id := projectID(t)

	c.createProject(id)
	c.run(id, passed(1), failed(2))
	c.run(id, passed(1), passed(2))

	c.do(c.request(http.MethodPost, "/projects/"+id+"/history/clean", nil), http.StatusAccepted)
	c.requireSucceeded(id)

	var builds struct {
		Builds []string `json:"builds"`
	}
	c.getJSON("/projects/"+id, &builds)
	if want := []string{"latest", "1"}; !slices.Equal(builds.Builds, want) {
		t.Errorf("project lists builds %v after clearing history, want %v", builds.Builds, want)
	}
	c.do(c.request(http.MethodGet, "/projects/"+id+"/reports/2/index.html", nil), http.StatusNotFound)

	report := "/projects/" + id + "/reports/latest/"
	if h := c.testResult(report, "Test 2").History; len(h) != 0 {
		t.Errorf("Test 2 still has %d history entries after clearing history, want none: %+v", len(h), h)
	}

	var sum summary
	c.getJSON(report+"summary.json", &sum)
	if sum.Stats.Total != 2 || sum.Stats.Passed != 2 {
		t.Errorf("rebuilt report counts %+v, want the last run's results: 2 passed", sum.Stats)
	}
}
