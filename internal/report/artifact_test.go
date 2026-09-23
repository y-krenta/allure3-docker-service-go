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

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

//

//

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
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing result file: %v", err)
	}
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
	if err := projects.CreateDir(dir, projectID); err != nil {
		t.Fatalf("CreateDir(%q) = %v", projectID, err)
	}
	for n := 1; n <= 3; n++ {
		writeRealResult(t, dir, projectID, n)
	}

	g := New(dir, allure, testHistoryLimit, testBaseURL)
	for build := 1; build <= 2; build++ {
		if err := g.Generate(t.Context(), projectID); err != nil {
			t.Fatalf("Generate (build %d) = %v, want nil", build, err)
		}
	}
	return dir, projectID
}

//

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
	if err != nil {
		t.Fatalf("walking the built report: %v", err)
	}
	collect(projects.HistoryFile(dir, projectID))

	return found
}

//

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

	if inTestResults == 0 {
		t.Fatalf("no urls under data/test-results in the built report; found %d elsewhere, "+
			"which is not the file a test's history panel reads", len(found))
	}
	sort.Strings(urls)
	return urls
}

//

func urlsInJSONFile(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

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

//

func TestGeneratedReportCarriesOnlyAbsoluteURLs(t *testing.T) {
	urls := requireTestResultURLs(t, buildReportWithHistory(t))

	for _, u := range urls {
		if !strings.HasPrefix(u, testBaseURL+"/") {
			t.Errorf("report url %q, want one built from the configured base %q", u, testBaseURL)
		}
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
	if err := os.WriteFile(harness, []byte(body.String()), 0o644); err != nil {
		t.Fatalf("writing harness: %v", err)
	}

	out, err := exec.CommandContext(t.Context(), node, harness).CombinedOutput()
	if err != nil {
		t.Fatalf("new URL() rejected a url the report carries, which is what kills the page:\n%s\nurls:\n%s",
			out, strings.Join(urls, "\n"))
	}
	if got := strings.TrimSpace(string(out)); got != "ok" {
		t.Errorf("harness said %q, want ok", got)
	}
}

func TestGeneratedHistoryLinksOpenThePastReport(t *testing.T) {
	dir, projectID := generateTwice(t)

	entries, err := os.ReadDir(filepath.Join(projects.NumberedReportDir(dir, projectID, 1), "data", "test-results"))
	if err != nil {
		t.Fatalf("reading build 1's test results: %v", err)
	}
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
			if id, ok := strings.CutPrefix(u, prefix); !ok || !pastIDs[id] {
				t.Errorf("%s: history link %q, want %s<id of a test in build 1>", filepath.Base(path), u, prefix)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking build 2's test results: %v", err)
	}
	if links == 0 {
		t.Fatal("no history links in build 2's test results")
	}

	want := map[string]bool{
		reportURLFor(testBaseURL, projectID, 1): false,
		reportURLFor(testBaseURL, projectID, 2): false,
	}
	for _, u := range urlsInJSONFile(t, projects.HistoryFile(dir, projectID)) {
		if _, ok := want[u]; !ok {
			t.Errorf("history.jsonl url %q, want one of the two builds' report urls", u)
			continue
		}
		want[u] = true
	}
	for u, seen := range want {
		if !seen {
			t.Errorf("history.jsonl has no entry for %q", u)
		}
	}
}
