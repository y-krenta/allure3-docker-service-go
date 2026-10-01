package e2e

import (
	"slices"
	"strings"
	"testing"
)

func TestHistoryAccumulatesAcrossRuns(t *testing.T) {
	c := newClient(t)
	id := projectID(t)

	c.createProject(id)
	for _, run := range [][]result{
		{passed(1), failed(2), passed(3)},
		{passed(1), passed(2)},
	} {
		c.clearResults(id)
		c.upload(id, run...)
		c.startGeneration(id)
		c.requireSucceeded(id)
	}

	var builds struct {
		Builds []string `json:"builds"`
	}
	c.getJSON("/projects/"+id, &builds)
	if want := []string{"latest", "2", "1"}; !slices.Equal(builds.Builds, want) {
		t.Errorf("project lists builds %v, want %v", builds.Builds, want)
	}

	var latest summary
	c.getJSON("/projects/"+id+"/reports/latest/summary.json", &latest)
	if latest.Stats.Total != 2 || latest.Stats.Passed != 2 {
		t.Errorf("latest report counts %+v, want only the second run: 2 passed", latest.Stats)
	}

	reportsPath := "/projects/" + id + "/reports/"
	test2 := c.testResult(reportsPath+"latest/", "Test 2")
	if test2.Status != "passed" {
		t.Errorf("Test 2 in the latest report is %q, want passed", test2.Status)
	}
	if len(test2.History) != 1 {
		t.Fatalf("Test 2 has %d history entries, want the one earlier run", len(test2.History))
	}
	past := test2.History[0]
	if past.Status != "failed" {
		t.Errorf("Test 2's earlier run is %q in its history, want failed", past.Status)
	}

	prefix := baseURL + reportsPath + "1/index.html#"
	pastPage, ok := strings.CutPrefix(past.URL, prefix)
	if !ok {
		t.Fatalf("Test 2's history links to %q, want %s<test id>", past.URL, prefix)
	}
	if pastPage == "" {
		t.Errorf("Test 2's history link %q names no test", past.URL)
	}
	c.get(reportsPath + "1/index.html")

	var first summary
	c.getJSON(reportsPath+"1/summary.json", &first)
	if first.Stats.Total != 3 || first.Stats.Passed != 2 || first.Stats.Failed != 1 {
		t.Errorf("build 1 counts %+v, want the first run: 2 passed, 1 failed", first.Stats)
	}
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
	c.t.Fatalf("no test called %q in the report at %s", name, reportPath)
	return treeLeaf{}
}
