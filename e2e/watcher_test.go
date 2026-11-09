package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatcherBuildsUploadedResults(t *testing.T) {
	c := newClient(t)
	if watcherURL == "" {
		t.Skip("E2E_WATCHER_URL is not set: no service with the watcher on to test")
	}
	c.base = watcherURL
	id := projectID(t)

	c.createProject(id)

	c.upload(id, passed(1), failed(2))
	c.waitForReport(id, 2, 1, 1)

	c.clearResults(id)
	c.upload(id, passed(1), passed(2), passed(3))
	c.waitForReport(id, 3, 3, 0)

	var builds struct {
		Builds []string `json:"builds"`
	}
	c.getJSON("/projects/"+id, &builds)
	assert.Contains(t, builds.Builds, "1", "want the first run archived as 1")
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
	require.FailNow(c.t, "watcher built no matching report within a minute",
		"%s: want %d passed, %d failed; last seen %+v", id, passed, failed, last.Stats)
}

func (c *client) latestSummary(id string) (summary, bool) {
	c.t.Helper()

	req := c.request(http.MethodGet, "/projects/"+id+"/reports/latest/summary.json", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err, "GET %s", req.URL.Path)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return summary{}, false
	}
	body, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err, "GET %s: reading body", req.URL.Path)
	require.Equal(c.t, http.StatusOK, resp.StatusCode, "GET %s, want 200 or 404\n%s", req.URL.Path, body)
	var sum summary
	require.NoError(c.t, json.Unmarshal(body, &sum), "GET %s: body is not the expected JSON\n%s", req.URL.Path, body)
	return sum, true
}
