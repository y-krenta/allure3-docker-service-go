package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type browserDriver struct {
	bin    string
	envDir string
	args   func(port int) []string
	caps   map[string]any
}

var browserDrivers = map[string]browserDriver{
	"chrome": {
		bin:    "chromedriver",
		envDir: "CHROMEWEBDRIVER",
		args:   func(port int) []string { return []string{"--port=" + strconv.Itoa(port)} },
		caps: map[string]any{
			"browserName":        "chrome",
			"goog:chromeOptions": map[string]any{"args": []string{"--headless=new", "--no-sandbox", "--disable-gpu"}},
		},
	},
	"firefox": {
		bin:    "geckodriver",
		envDir: "GECKOWEBDRIVER",
		args:   func(port int) []string { return []string{"--port", strconv.Itoa(port)} },
		caps: map[string]any{
			"browserName":        "firefox",
			"moz:firefoxOptions": map[string]any{"args": []string{"-headless"}},
		},
	},
	"safari": {
		bin:  "safaridriver",
		args: func(port int) []string { return []string{"-p", strconv.Itoa(port)} },
		caps: map[string]any{"browserName": "safari"},
	},
}

func TestReportOpensATestInEachBrowser(t *testing.T) {
	names := strings.Fields(os.Getenv("E2E_BROWSERS"))
	if len(names) == 0 {
		t.Skip("E2E_BROWSERS is not set: the browser smokes run in CI")
	}
	c := newClient(t)
	id := projectID(t)

	c.createProject(id)
	c.run(id, passed(1), failed(2))
	c.run(id, passed(1), passed(2))

	reportPath := "/projects/" + id + "/reports/latest/"
	page := baseURL + reportPath + "index.html#" + c.leaf(reportPath, "Test 2").NodeID

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			d, ok := browserDrivers[name]
			if !ok {
				t.Fatalf("E2E_BROWSERS names %q, want chrome, firefox or safari", name)
			}
			s := openSession(t, d)
			s.call(http.MethodPost, "/url", map[string]any{"url": page}, nil)
			s.waitForText(testStepName)
		})
	}
}

type session struct {
	t   *testing.T
	url string
}

func openSession(t *testing.T, d browserDriver) *session {
	t.Helper()

	bin, err := exec.LookPath(d.bin)
	if err != nil && d.envDir != "" && os.Getenv(d.envDir) != "" {
		bin, err = exec.LookPath(filepath.Join(os.Getenv(d.envDir), d.bin))
	}
	if err != nil {
		t.Fatalf("%s not found: %v", d.bin, err)
	}

	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, d.args(port)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", d.bin, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(base + "/status")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer /status within 15s: %v", d.bin, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	var created struct {
		SessionID string `json:"sessionId"`
	}
	webdriver(t, t.Context(), http.MethodPost, base+"/session",
		map[string]any{"capabilities": map[string]any{"alwaysMatch": d.caps}}, &created)

	s := &session{t: t, url: base + "/session/" + created.SessionID}
	t.Cleanup(func() {
		webdriver(t, context.Background(), http.MethodDelete, s.url, nil, nil)
	})
	return s
}

func (s *session) call(method, path string, body, value any) {
	s.t.Helper()
	webdriver(s.t, s.t.Context(), method, s.url+path, body, value)
}

func (s *session) waitForText(text string) {
	s.t.Helper()

	var dom string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s.call(http.MethodPost, "/execute/sync",
			map[string]any{"script": "return document.documentElement.outerHTML", "args": []any{}}, &dom)
		if strings.Contains(dom, text) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	s.t.Fatalf("the page never showed %q within 30s - the report unmounted or never rendered the test\n%.2000s", text, dom)
}

func webdriver(t *testing.T, ctx context.Context, method, url string, body, value any) {
	t.Helper()

	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("webdriver %s %s: %v", method, url, err)
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		t.Fatalf("webdriver %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webdriver %s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("webdriver %s %s: reading body: %v", method, url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webdriver %s %s = %d\n%s", method, url, resp.StatusCode, raw)
	}
	if value == nil {
		return
	}
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("webdriver %s %s: answer is not JSON: %v\n%s", method, url, err, raw)
	}
	if err := json.Unmarshal(envelope.Value, value); err != nil {
		t.Fatalf("webdriver %s %s: unexpected value: %v\n%s", method, url, err, raw)
	}
}
