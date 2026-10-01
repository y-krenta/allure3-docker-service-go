package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"
)

func TestWatcherBuildsUploadedResults(t *testing.T) {
	c := newClient(t)
	if watcherURL == "" {
		t.Skip("E2E_WATCHER_URL is not set: no service with the watcher on to test")
	}
	c.base = watcherURL
	id := projectID(t)

	c.createProject(id)
	time.Sleep(2 * time.Second)

	c.upload(id, passed(1), failed(2))
	c.waitForReport(id, 2, 1, 1)

	c.clearResults(id)
	c.upload(id, passed(1), passed(2), passed(3))
	c.waitForReport(id, 3, 3, 0)

	var builds struct {
		Builds []string `json:"builds"`
	}
	c.getJSON("/projects/"+id, &builds)
	if !slices.Contains(builds.Builds, "1") {
		t.Errorf("project lists builds %v, want the first run archived as 1", builds.Builds)
	}
}

func (c *client) waitForReport(id string, total, passed, failed int) {
	c.t.Helper()

	var last summary
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if sum, ok := c.latestSummary(id); ok {
			last = sum
			if sum.Stats.Total == total && sum.Stats.Passed == passed && sum.Stats.Failed == failed {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	c.t.Fatalf("watcher built no report of %d passed, %d failed for %s within a minute; last seen %+v",
		passed, failed, id, last.Stats)
}

func (c *client) latestSummary(id string) (summary, bool) {
	c.t.Helper()

	req := c.request(http.MethodGet, "/projects/"+id+"/reports/latest/summary.json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("GET %s: %v", req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return summary{}, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("GET %s: reading body: %v", req.URL.Path, err)
	}
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("GET %s = %d, want 200 or 404\n%s", req.URL.Path, resp.StatusCode, body)
	}
	var sum summary
	if err := json.Unmarshal(body, &sum); err != nil {
		c.t.Fatalf("GET %s: body is not the expected JSON: %v\n%s", req.URL.Path, err, body)
	}
	return sum, true
}
