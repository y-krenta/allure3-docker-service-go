package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, []string{"latest", "1"}, builds.Builds)
	c.do(c.request(http.MethodGet, "/projects/"+id+"/reports/2/index.html", nil), http.StatusNotFound)

	report := "/projects/" + id + "/reports/latest/"
	assert.Empty(t, c.testResult(report, "Test 2").History)

	var sum summary
	c.getJSON(report+"summary.json", &sum)
	assert.Equal(t, 2, sum.Stats.Total)
	assert.Equal(t, 2, sum.Stats.Passed)
}
