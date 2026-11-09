package e2e

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHistoryAccumulatesAcrossRuns(t *testing.T) {
	c := newClient(t)
	id := projectID(t)

	c.createProject(id)
	c.run(id, passed(1), failed(2), passed(3))
	c.run(id, passed(1), passed(2))

	var builds struct {
		Builds []string `json:"builds"`
	}
	c.getJSON("/projects/"+id, &builds)
	assert.Equal(t, []string{"latest", "2", "1"}, builds.Builds)

	var latest summary
	c.getJSON("/projects/"+id+"/reports/latest/summary.json", &latest)
	assert.Equal(t, 2, latest.Stats.Total, "want only the second run")
	assert.Equal(t, 2, latest.Stats.Passed, "want only the second run")

	reportsPath := "/projects/" + id + "/reports/"
	test2 := c.testResult(reportsPath+"latest/", "Test 2")
	assert.Equal(t, "passed", test2.Status)
	require.Len(t, test2.History, 1, "want the one earlier run")
	past := test2.History[0]
	assert.Equal(t, "failed", past.Status)

	prefix := baseURL + reportsPath + "1/index.html#"
	pastPage, ok := strings.CutPrefix(past.URL, prefix)
	require.True(t, ok, "Test 2's history links to %q, want %s<test id>", past.URL, prefix)
	assert.NotEmpty(t, pastPage, "Test 2's history link names no test")
	c.get(reportsPath + "1/index.html")

	var first summary
	c.getJSON(reportsPath+"1/summary.json", &first)
	assert.Equal(t, 3, first.Stats.Total)
	assert.Equal(t, 2, first.Stats.Passed)
	assert.Equal(t, 1, first.Stats.Failed)
}

type testResult struct {
	Status  string `json:"status"`
	History []struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	} `json:"history"`
}

func (c *client) testResult(reportPath, name string) testResult {
	c.t.Helper()

	var r testResult
	c.getJSON(reportPath+"data/test-results/"+c.leaf(reportPath, name).NodeID+".json", &r)
	return r
}

type treeLeaf struct {
	NodeID     string `json:"nodeId"`
	Name       string `json:"name"`
	Transition string `json:"transition"`
}

func (c *client) leaf(reportPath, name string) treeLeaf {
	c.t.Helper()

	var tree struct {
		LeavesByID map[string]treeLeaf `json:"leavesById"`
	}
	c.getJSON(reportPath+"widgets/tree.json", &tree)

	for _, leaf := range tree.LeavesByID {
		if leaf.Name == name {
			return leaf
		}
	}
	require.FailNow(c.t, "no such test in the report", "%q at %s", name, reportPath)
	return treeLeaf{}
}
