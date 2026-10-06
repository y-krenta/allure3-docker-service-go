package e2e

import (
	"slices"
	"strings"
	"testing"
)

// summary is the slice of a report's summary.json these tests read: the
// counts Allure computed from the results it was given.
type summary struct {
	Stats struct {
		Total  int `json:"total"`
		Passed int `json:"passed"`
		Failed int `json:"failed"`
	} `json:"stats"`
}

// TestCIPipeline walks the report step from the README, start to finish:
// create the project, clear the previous execution's results, upload this
// one's, start a build, wait for it, and open the report. The report has to
// count exactly the results uploaded - the service's job is to get them from
// the pipeline into Allure and the finished report back out, and a report of
// anything else would pass every other check here.
func TestCIPipeline(t *testing.T) {
	c := newClient(t)
	id := projectID(t)

	c.createProject(id)
	c.clearResults(id)
	c.upload(id, passed(1), passed(2), failed(3))
	c.startGeneration(id)
	c.requireSucceeded(id)

	resp, page := c.get("/projects/" + id + "/latest-report")
	if want := "/projects/" + id + "/reports/latest/"; resp.Request.URL.Path != want {
		t.Errorf("latest-report led to %s, want %s", resp.Request.URL.Path, want)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("report page Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(string(page), "<html") {
		t.Errorf("report page is not an HTML document:\n%.300s", page)
	}

	var sum summary
	c.getJSON("/projects/"+id+"/reports/latest/summary.json", &sum)
	if sum.Stats.Total != 3 || sum.Stats.Passed != 2 || sum.Stats.Failed != 1 {
		t.Errorf("report counts %+v, want total 3, passed 2, failed 1", sum.Stats)
	}

	var builds struct {
		Builds []string `json:"builds"`
	}
	c.getJSON("/projects/"+id, &builds)
	if !slices.Contains(builds.Builds, "latest") {
		t.Errorf("project lists builds %v, want latest among them", builds.Builds)
	}
}
