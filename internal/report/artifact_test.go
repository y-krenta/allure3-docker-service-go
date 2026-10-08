package report

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

func requireAllureCLI(t *testing.T) string {
	t.Helper()

	path, err := exec.LookPath("allure")
	if err != nil {
		t.Skip("allure CLI is not installed, cannot build a real report")
	}
	return path
}

const testStepName = "the step only an opened test shows"

func writeRealResult(t *testing.T, baseDir, projectID string, n int) {
	t.Helper()

	uuid := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	body := fmt.Sprintf(`{
		"uuid": %q,
		"historyId": "case-%d",
		"fullName": "suite.Test%d",
		"name": "Test %d",
		"status": "passed",
		"stage": "finished",
		"start": 1700000000000,
		"stop": 1700000000250,
		"steps": [
			{
				"name": %q,
				"status": "passed",
				"stage": "finished",
				"start": 1700000000000,
				"stop": 1700000000100
			}
		]
	}`, uuid, n, n, n, testStepName)

	path := filepath.Join(projects.ResultsDir(baseDir, projectID), uuid+"-result.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

type foundURL struct {
	file string
	url  string
}

func generateTwice(t *testing.T) (dir, projectID string) {
	t.Helper()

	allure := requireAllureCLI(t)

	dir = t.TempDir()
	projectID = "demo"
	require.NoError(t, projects.CreateDir(dir, projectID))
	for n := 1; n <= 3; n++ {
		writeRealResult(t, dir, projectID, n)
	}

	g := New(dir, allure, testHistoryLimit, testBaseURL, testMaxBuilds, 0)
	for build := 1; build <= 2; build++ {
		require.NoError(t, g.Generate(t.Context(), projectID), "build %d", build)
	}
	return dir, projectID
}

func buildReportWithHistory(t *testing.T) []foundURL {
	t.Helper()

	dir, projectID := generateTwice(t)

	var found []foundURL
	collect := func(path string) {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		for _, u := range urlsInJSONFile(t, path) {
			found = append(found, foundURL{file: filepath.ToSlash(rel), url: u})
		}
	}

	latest := projects.LatestReportDir(dir, projectID)
	err := filepath.WalkDir(latest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			collect(path)
		}
		return nil
	})
	require.NoError(t, err)
	collect(projects.HistoryFile(dir, projectID))

	return found
}

func requireTestResultURLs(t *testing.T, found []foundURL) []string {
	t.Helper()

	urls := make([]string, 0, len(found))
	seen := map[string]bool{}
	inTestResults := 0
	for _, f := range found {
		if strings.Contains(f.file, "/data/test-results/") {
			inTestResults++
		}
		if !seen[f.url] {
			seen[f.url] = true
			urls = append(urls, f.url)
		}
	}

	require.NotZero(t, inTestResults, "no urls under data/test-results in the built report; found %d elsewhere, "+
		"which is not the file a test's history panel reads", len(found))
	sort.Strings(urls)
	return urls
}

func urlsInJSONFile(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)

	var found []string
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			for key, value := range v {
				if s, ok := value.(string); ok && key == "url" {
					found = append(found, s)
					continue
				}
				walk(value)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var doc any
		if err := json.Unmarshal([]byte(line), &doc); err != nil {

			return found
		}
		walk(doc)
	}
	return found
}

func TestGeneratedReportCarriesOnlyAbsoluteURLs(t *testing.T) {
	urls := requireTestResultURLs(t, buildReportWithHistory(t))

	for _, u := range urls {
		assert.True(t, strings.HasPrefix(u, testBaseURL+"/"), "report url %q, want one built from the configured base %q", u, testBaseURL)
	}
}

func TestGeneratedReportURLsSurviveNewURL(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, cannot exercise new URL()")
	}

	urls := requireTestResultURLs(t, buildReportWithHistory(t))

	var body strings.Builder
	for _, u := range urls {
		body.WriteString("new URL(" + strconv.Quote(u) + ");\n")
	}
	body.WriteString("console.log(\"ok\");\n")

	harness := filepath.Join(t.TempDir(), "harness.mjs")
	require.NoError(t, os.WriteFile(harness, []byte(body.String()), 0o644))

	out, err := exec.CommandContext(t.Context(), node, harness).CombinedOutput()
	require.NoError(t, err, "new URL() rejected a url the report carries, which is what kills the page:\n%s\nurls:\n%s",
		out, strings.Join(urls, "\n"))
	assert.Equal(t, "ok", strings.TrimSpace(string(out)))
}

func TestGeneratedHistoryLinksOpenThePastReport(t *testing.T) {
	dir, projectID := generateTwice(t)

	entries, err := os.ReadDir(filepath.Join(projects.NumberedReportDir(dir, projectID, 1), "data", "test-results"))
	require.NoError(t, err)
	pastIDs := map[string]bool{}
	for _, e := range entries {
		pastIDs[strings.TrimSuffix(e.Name(), ".json")] = true
	}

	prefix := reportURLFor(testBaseURL, projectID, 1) + "#"
	links := 0
	latest := filepath.Join(projects.LatestReportDir(dir, projectID), "data", "test-results")
	err = filepath.WalkDir(latest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		for _, u := range urlsInJSONFile(t, path) {
			links++
			id, ok := strings.CutPrefix(u, prefix)
			assert.True(t, ok && pastIDs[id], "%s: history link %q, want %s<id of a test in build 1>", filepath.Base(path), u, prefix)
		}
		return nil
	})
	require.NoError(t, err)
	require.NotZero(t, links, "no history links in build 2's test results")

	want := []string{reportURLFor(testBaseURL, projectID, 1), reportURLFor(testBaseURL, projectID, 2)}
	got := urlsInJSONFile(t, projects.HistoryFile(dir, projectID))
	assert.Subset(t, want, got, "history.jsonl carries a url that is neither build's report")
	assert.Subset(t, got, want, "history.jsonl misses a build's report")
}
