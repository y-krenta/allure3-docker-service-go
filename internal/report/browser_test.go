package report

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

//

var chromeCandidates = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"google-chrome",
	"google-chrome-stable",
	"chromium",
	"chromium-browser",
}

func requireChrome(t *testing.T) string {
	t.Helper()

	for _, candidate := range chromeCandidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("no Chrome or Chromium found, cannot open the report in a browser")
	return ""
}

func anOpenedTest(t *testing.T, reportDir string) (id, name string) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(reportDir, "data", "test-results"))
	if err != nil {
		t.Fatalf("reading the report's test results: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(reportDir, "data", "test-results", e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		var result struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("test result %s is not valid JSON: %v", e.Name(), err)
		}
		if result.Name == "" {
			continue
		}
		return strings.TrimSuffix(e.Name(), ".json"), result.Name
	}

	t.Fatal("the built report has no test results to open")
	return "", ""
}

//

func TestReportOpensATestInABrowser(t *testing.T) {
	chrome := requireChrome(t)

	dir, projectID := generateTwice(t)
	reportDir := filepath.Join(dir, projectID, "reports", "latest")

	id, name := anOpenedTest(t, reportDir)

	srv := httptest.NewServer(http.FileServer(http.Dir(reportDir)))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, chrome,
		"--headless",
		"--disable-gpu",

		"--no-sandbox",

		"--user-data-dir="+t.TempDir(),

		"--virtual-time-budget=15000",
		"--enable-logging=stderr",
		"--log-level=0",
		"--dump-dom",
		srv.URL+"/#"+id,
	)

	stdout := newDOMWriter()
	var stderr strings.Builder
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting chrome: %v", err)
	}

	select {
	case <-stdout.done:
	case <-ctx.Done():
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	dom := stdout.String()
	if !strings.Contains(dom, "</html>") {
		t.Fatalf("chrome printed no complete DOM within the timeout\n%s", stderr.String())
	}

	if !strings.Contains(dom, testStepName) {
		t.Errorf("the page opened at test %q does not show that test's step; the report either unmounted or never rendered the test\nconsole:\n%s",
			name, consoleLines(stderr.String()))
	}

	for _, line := range consoleErrors(stderr.String()) {
		t.Errorf("the report logged an error while opening a test: %s", line)
	}
}

type domWriter struct {
	mu   sync.Mutex
	buf  strings.Builder
	done chan struct{}
	once sync.Once
}

func newDOMWriter() *domWriter {
	return &domWriter{done: make(chan struct{})}
}

func (w *domWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf.Write(p)

	if strings.Contains(w.buf.String(), "</html>") {
		w.once.Do(func() { close(w.done) })
	}
	return len(p), nil
}

func (w *domWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.String()
}

func consoleLines(stderr string) string {
	var kept []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, ":CONSOLE:") {
			kept = append(kept, strings.TrimSpace(line))
		}
	}
	if len(kept) == 0 {
		return "(empty)"
	}
	return strings.Join(kept, "\n")
}

func consoleErrors(stderr string) []string {
	var errs []string
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.Contains(line, ":CONSOLE:") {
			continue
		}
		if strings.Contains(line, "Uncaught") {
			errs = append(errs, strings.TrimSpace(line))
		}
	}
	return errs
}
