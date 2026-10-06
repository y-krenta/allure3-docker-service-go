package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMergeRequestSeededFromMaster(t *testing.T) {
	c := newClient(t)
	master, mr := projectID(t)+"-master", projectID(t)+"-mr"

	c.createProject(master)
	c.upload(master, passed(1), passed(2))
	c.startGeneration(master)
	c.requireSucceeded(master)

	c.createProject(mr)
	c.seedHistory(mr, master)
	c.upload(mr, passed(1), failed(2))
	c.startGeneration(mr)
	c.requireSucceeded(mr)

	report := "/projects/" + mr + "/reports/latest/"
	if got := c.leaf(report, "Test 2").Transition; got != "regressed" {
		t.Errorf("Test 2, passing on master and failing in the MR, has transition %q, want regressed", got)
	}
	if got := c.leaf(report, "Test 1").Transition; got != "" {
		t.Errorf("Test 1, passing on both, has transition %q, want none", got)
	}
}

func (c *client) seedHistory(target, source string) {
	c.t.Helper()
	body := fmt.Sprintf(`{"from_project_id": %q}`, source)
	req := c.request(http.MethodPost, "/projects/"+target+"/history/seed", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.do(req, http.StatusNoContent)
}
