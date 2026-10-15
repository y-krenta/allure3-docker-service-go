package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, "regressed", c.leaf(report, "Test 2").Transition, "Test 2 passes on master and fails in the MR")
	assert.Empty(t, c.leaf(report, "Test 1").Transition, "Test 1 passes on both")
}

func (c *client) seedHistory(target, source string) {
	c.t.Helper()
	body := fmt.Sprintf(`{"from_project_id": %q}`, source)
	req := c.request(http.MethodPost, "/projects/"+target+"/history/seed", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.do(req, http.StatusNoContent)
}
