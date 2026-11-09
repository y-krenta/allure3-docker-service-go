package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	baseURL    string
	watcherURL string

	skipReason string
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if u := os.Getenv("E2E_BASE_URL"); u != "" {
		baseURL = strings.TrimRight(u, "/")
		watcherURL = strings.TrimRight(os.Getenv("E2E_WATCHER_URL"), "/")
		return m.Run()
	}

	if _, err := exec.LookPath("allure"); err != nil {
		skipReason = "allure CLI not on PATH, cannot start the service"
		return m.Run()
	}

	svc, err := startService()
	if err != nil {
		log.Printf("e2e: starting the service: %v", err)
		return 1
	}
	defer svc.stop()

	watcher, err := startService("CHECK_RESULTS_EVERY_SECONDS=1")
	if err != nil {
		log.Printf("e2e: starting the service with the watcher on: %v", err)
		return 1
	}
	defer watcher.stop()

	baseURL = svc.url
	watcherURL = watcher.url
	code := m.Run()
	if code != 0 {
		log.Printf("--- service log ---\n%s", svc.log())
		log.Printf("--- watcher service log ---\n%s", watcher.log())
	}
	return code
}

type service struct {
	url     string
	cmd     *exec.Cmd
	dir     string
	logPath string

	exited <-chan error
}

func startService(env ...string) (_ *service, err error) {
	dir, err := os.MkdirTemp("", "allure-e2e-*")
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	bin := filepath.Join(dir, "allure-service")
	build := exec.Command("go", "build", "-o", bin, "../cmd/allure-service")
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go build: %v\n%s", err, out)
	}

	port, err := freePort()
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)

	logPath := filepath.Join(dir, "service.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}

	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"PUBLIC_BASE_URL="+url,
		"STATIC_CONTENT_PROJECTS="+filepath.Join(dir, "projects"),
		"MAX_CONCURRENT_BUILDS=1",
		"CHECK_RESULTS_EVERY_SECONDS=0",
	)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	s := &service{url: url, cmd: cmd, dir: dir, logPath: logPath, exited: exited}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return nil, fmt.Errorf("service exited during startup: %v\n%s", err, s.log())
		default:
		}
		resp, err := http.Get(url + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	_ = cmd.Process.Kill()
	<-exited
	return nil, fmt.Errorf("service did not answer /health within 30s\n%s", s.log())
}

func (s *service) stop() {
	_ = s.cmd.Process.Signal(os.Interrupt)
	select {
	case <-s.exited:
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
	}
	_ = os.RemoveAll(s.dir)
}

func (s *service) log() string {
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		return fmt.Sprintf("(reading the service log: %v)", err)
	}
	return string(data)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

type client struct {
	t    *testing.T
	base string
}

func newClient(t *testing.T) *client {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipReason)
	}
	return &client{t: t, base: baseURL}
}

func projectID(t *testing.T) string {
	slug := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(t.Name()), "-")
	slug = strings.Trim(slug, "-")
	return fmt.Sprintf("%s-%d", slug, time.Now().UnixNano())
}

func (c *client) do(req *http.Request, want int) (*http.Response, []byte) {
	c.t.Helper()

	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err, "%s %s", req.Method, req.URL.Path)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err, "%s %s: reading body", req.Method, req.URL.Path)
	require.Equal(c.t, want, resp.StatusCode, "%s %s\n%s", req.Method, req.URL.Path, body)
	return resp, body
}

func (c *client) request(method, path string, body io.Reader) *http.Request {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+path, body)
	require.NoError(c.t, err)
	return req
}

func (c *client) get(path string) (*http.Response, []byte) {
	c.t.Helper()
	return c.do(c.request(http.MethodGet, path, nil), http.StatusOK)
}

func (c *client) getJSON(path string, v any) {
	c.t.Helper()
	_, body := c.get(path)
	require.NoError(c.t, json.Unmarshal(body, v), "GET %s: body is not the expected JSON\n%s", path, body)
}

func (c *client) createProject(id string) {
	c.t.Helper()
	body := fmt.Sprintf(`{"project_id": %q}`, id)
	req := c.request(http.MethodPost, "/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.do(req, http.StatusCreated)
}

func (c *client) clearResults(id string) {
	c.t.Helper()
	c.do(c.request(http.MethodDelete, "/projects/"+id+"/results", nil), http.StatusNoContent)
}

func (c *client) upload(id string, results ...result) {
	c.t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, r := range results {
		fw, err := mw.CreateFormFile("files[]", r.uuid+"-result.json")
		require.NoError(c.t, err)
		_, err = fw.Write(r.json())
		require.NoError(c.t, err)
	}
	require.NoError(c.t, mw.Close())

	req := c.request(http.MethodPost, "/projects/"+id+"/results", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	_, body := c.do(req, http.StatusOK)

	var resp struct {
		Count int `json:"processed_files_count"`
	}
	require.NoError(c.t, json.Unmarshal(body, &resp), "upload answer is not the expected JSON\n%s", body)
	require.Equal(c.t, len(results), resp.Count, "processed files\n%s", body)
}

func (c *client) startGeneration(id string) {
	c.t.Helper()
	c.do(c.request(http.MethodPost, "/projects/"+id+"/generation", nil), http.StatusAccepted)
}

func (c *client) run(id string, results ...result) {
	c.t.Helper()
	c.clearResults(id)
	c.upload(id, results...)
	c.startGeneration(id)
	c.requireSucceeded(id)
}

type generation struct {
	State string `json:"state"`
	Error string `json:"error"`
}

func (c *client) waitForBuild(id string) generation {
	c.t.Helper()

	ctx, cancel := context.WithTimeout(c.t.Context(), 2*time.Minute)
	defer cancel()
	for {
		var st generation
		c.getJSON("/projects/"+id+"/generation", &st)
		if st.State != "running" {
			return st
		}
		select {
		case <-ctx.Done():
			require.FailNow(c.t, "build still running after 2 minutes", id)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (c *client) requireSucceeded(id string) {
	c.t.Helper()
	st := c.waitForBuild(id)
	require.Equal(c.t, "succeeded", st.State, "build of %s\n%s", id, st.Error)
}

type result struct {
	uuid   string
	name   string
	status string
}

const testStepName = "the step only an opened test shows"

func passed(n int) result { return newResult(n, "passed") }
func failed(n int) result { return newResult(n, "failed") }

var resultSeq atomic.Int64

func newResult(n int, status string) result {
	return result{
		uuid:   fmt.Sprintf("00000000-0000-4000-8000-%012d", resultSeq.Add(1)),
		name:   fmt.Sprintf("Test %d", n),
		status: status,
	}
}

func (r result) json() []byte {
	data, err := json.Marshal(map[string]any{
		"uuid":      r.uuid,
		"historyId": "case-" + r.name,
		"fullName":  "suite." + r.name,
		"name":      r.name,
		"status":    r.status,
		"stage":     "finished",
		"start":     1700000000000,
		"stop":      1700000000250,
		"steps": []map[string]any{{
			"name":   testStepName,
			"status": r.status,
			"stage":  "finished",
			"start":  1700000000000,
			"stop":   1700000000100,
		}},
	})
	if err != nil {
		panic(errors.Join(errors.New("marshalling a fixed fixture"), err))
	}
	return data
}
