package e2e

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, "/projects/"+id+"/reports/latest/", resp.Request.URL.Path)
	ct := resp.Header.Get("Content-Type")
	assert.True(t, strings.HasPrefix(ct, "text/html"), "report page Content-Type = %q, want text/html", ct)
	assert.Contains(t, string(page), "<html")

	var sum summary
	c.getJSON("/projects/"+id+"/reports/latest/summary.json", &sum)
	assert.Equal(t, 3, sum.Stats.Total)
	assert.Equal(t, 2, sum.Stats.Passed)
	assert.Equal(t, 1, sum.Stats.Failed)

	var builds struct {
		Builds []string `json:"builds"`
	}
	c.getJSON("/projects/"+id, &builds)
	assert.Contains(t, builds.Builds, "latest")
}
