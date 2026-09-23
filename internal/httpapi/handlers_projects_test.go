package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

func writeBuild(t *testing.T, dir, id, build string, modTime time.Time) string {
	t.Helper()

	buildDir := filepath.Join(projects.ReportsDir(dir, id), build)
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		t.Fatalf("mkdir %q: %v", buildDir, err)
	}

	index := filepath.Join(buildDir, "index.html")
	if err := os.WriteFile(index, []byte("<h1>"+build+"</h1>"), 0644); err != nil {
		t.Fatalf("write %q: %v", index, err)
	}
	if err := os.Chtimes(index, modTime, modTime); err != nil {
		t.Fatalf("chtimes %q: %v", index, err)
	}

	return buildDir
}

func callWithPath(h http.HandlerFunc, method, target string, body io.Reader, pathValues map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	for k, v := range pathValues {
		r.SetPathValue(k, v)
	}

	w := httptest.NewRecorder()
	h(w, r)

	return w
}

func TestCreateProject(t *testing.T) {
	t.Run("creates the project tree", func(t *testing.T) {
		s, dir := newTestServer(t)

		w := callWithPath(s.createProject, http.MethodPost, "/projects", strings.NewReader(`{"project_id":"demo"}`), nil)

		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body)
		}
		if info, err := os.Stat(projects.ResultsDir(dir, "demo")); err != nil || !info.IsDir() {
			t.Errorf("results dir missing: stat err = %v", err)
		}
	})

	t.Run("rejects a malformed body", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.createProject, http.MethodPost, "/projects", strings.NewReader(`{"project_id":`), nil)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.createProject, http.MethodPost, "/projects", strings.NewReader(`{"project_id":"BAD ID!"}`), nil)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("existing project conflicts", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")

		w := callWithPath(s.createProject, http.MethodPost, "/projects", strings.NewReader(`{"project_id":"demo"}`), nil)

		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
		}
	})
}

func TestListProjects(t *testing.T) {
	decode := func(t *testing.T, w *httptest.ResponseRecorder) listProjectsResponse {
		t.Helper()

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}

		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var got listProjectsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", w.Body, err)
		}

		return got
	}

	t.Run("empty dir returns an empty array, not null", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.listProjects, http.MethodGet, "/projects", nil, nil)

		if got := decode(t, w); len(got.Projects) != 0 {
			t.Fatalf("projects = %v, want empty", got.Projects)
		}
		if body := strings.TrimSpace(w.Body.String()); body != `{"projects":[]}` {
			t.Errorf("body = %s, want an empty JSON array", body)
		}
	})

	t.Run("lists directories only", func(t *testing.T) {
		s, dir := newTestServer(t, "alpha", "beta")
		if err := os.WriteFile(filepath.Join(dir, "stray.txt"), nil, 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		w := callWithPath(s.listProjects, http.MethodGet, "/projects", nil, nil)

		got := decode(t, w)
		if len(got.Projects) != 2 {
			t.Fatalf("projects = %v, want alpha and beta only", got.Projects)
		}
	})

	t.Run("search matches case-insensitively", func(t *testing.T) {
		s, _ := newTestServer(t, "alpha", "beta", "alphabet")

		w := callWithPath(s.listProjects, http.MethodGet, "/projects?search=ALPHA", nil, nil)

		got := decode(t, w)
		if len(got.Projects) != 2 {
			t.Fatalf("projects = %v, want the two alpha* projects", got.Projects)
		}
		for _, id := range got.Projects {
			if !strings.Contains(id, "alpha") {
				t.Errorf("projects contain %q, which does not match the search", id)
			}
		}
	})

	t.Run("unreadable projects dir is a server error", func(t *testing.T) {
		s := NewServer(filepath.Join(t.TempDir(), "does-not-exist"), nil, RuntimeConfig{}, Versions{})

		w := callWithPath(s.listProjects, http.MethodGet, "/projects", nil, nil)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
	})
}

func TestDeleteProject(t *testing.T) {
	t.Run("removes the project", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/demo", nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNoContent, w.Body)
		}
		if _, err := os.Stat(filepath.Join(dir, "demo")); !os.IsNotExist(err) {
			t.Errorf("project dir still there (stat err = %v)", err)
		}
	})

	t.Run("default project is protected", func(t *testing.T) {
		s, dir := newTestServer(t, projects.DefaultProjectID)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/default", nil, map[string]string{"id": projects.DefaultProjectID})

		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
		}
		if _, err := os.Stat(filepath.Join(dir, projects.DefaultProjectID)); err != nil {
			t.Errorf("default project was touched: stat err = %v", err)
		}
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/BADID", nil, map[string]string{"id": "BADID"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("deleting an unknown project succeeds", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/nosuch", nil, map[string]string{"id": "nosuch"})

		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
		}
	})

	t.Run("removes through the generator, not behind its back", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/demo", nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNoContent, w.Body)
		}

		if len(gen.deletedWith) != 1 || gen.deletedWith[0] != "demo" {
			t.Errorf("Delete called with %v, want exactly one call for %q", gen.deletedWith, "demo")
		}
	})

	t.Run("a failed removal answers 500", func(t *testing.T) {
		gen := &stubGenerator{deleteErr: errors.New("boom")}
		s := newStubServer(gen)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/demo", nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
	})
}

func TestClearResults(t *testing.T) {
	t.Run("clears and answers 204 with no body", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.clearResults, http.MethodDelete, "/projects/demo/results",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNoContent, w.Body)
		}
		if got := w.Body.String(); got != "" {
			t.Errorf("body = %q, want empty", got)
		}
		if len(gen.clearedWith) != 1 || gen.clearedWith[0] != "demo" {
			t.Errorf("ClearResults called with %v, want exactly one call for %q", gen.clearedWith, "demo")
		}
	})

	t.Run("unknown project is not found", func(t *testing.T) {
		s := newStubServer(&stubGenerator{
			clearErr: fmt.Errorf("%w: demo", report.ErrProjectNotFound),
		})

		w := callWithPath(s.clearResults, http.MethodDelete, "/projects/demo/results",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body)
		}

		if body := w.Body.String(); strings.Contains(body, msgInternalError) {
			t.Errorf("body = %q, contains the 500 message too: a return is missing after the 404 write", body)
		}
	})

	t.Run("a server error keeps its cause to itself", func(t *testing.T) {
		s := newStubServer(&stubGenerator{
			clearErr: errors.New("removing /app/projects/demo/results/x.json: permission denied"),
		})

		w := callWithPath(s.clearResults, http.MethodDelete, "/projects/demo/results",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusInternalServerError, w.Body)
		}
		if body := w.Body.String(); strings.Contains(body, "/app/projects") {
			t.Errorf("body = %q, want no internal paths", body)
		}
	})

	t.Run("a malformed id never reaches the generator", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.clearResults, http.MethodDelete, "/projects/BAD_ID/results",
			nil, map[string]string{"id": "BAD_ID"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
		if len(gen.clearedWith) != 0 {
			t.Errorf("ClearResults was called with %v, want no call at all", gen.clearedWith)
		}
	})
}

func TestGetProject(t *testing.T) {
	builds := func(t *testing.T, w *httptest.ResponseRecorder) []string {
		t.Helper()

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var got projectBuildsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", w.Body, err)
		}

		return got.Builds
	}

	t.Run("sorts by index.html mtime with latest pinned first", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		now := time.Now()
		writeBuild(t, dir, "demo", "1", now.Add(-3*time.Hour))
		writeBuild(t, dir, "demo", "2", now.Add(-1*time.Hour))
		writeBuild(t, dir, "demo", "latest", now.Add(-2*time.Hour))

		w := callWithPath(s.getProject, http.MethodGet, "/projects/demo", nil, map[string]string{"id": "demo"})

		got := builds(t, w)
		want := []string{"latest", "2", "1"}
		if len(got) != len(want) {
			t.Fatalf("builds = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("builds = %v, want %v", got, want)
			}
		}
	})

	t.Run("skips build dirs without index.html", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "good", time.Now())
		if err := os.MkdirAll(filepath.Join(projects.ReportsDir(dir, "demo"), "empty"), 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}

		w := callWithPath(s.getProject, http.MethodGet, "/projects/demo", nil, map[string]string{"id": "demo"})

		got := builds(t, w)
		if len(got) != 1 || got[0] != "good" {
			t.Fatalf("builds = %v, want [good]", got)
		}
	})

	t.Run("skips files sitting next to build dirs", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "good", time.Now())
		if err := os.WriteFile(filepath.Join(projects.ReportsDir(dir, "demo"), "stray.txt"), nil, 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		w := callWithPath(s.getProject, http.MethodGet, "/projects/demo", nil, map[string]string{"id": "demo"})

		if got := builds(t, w); len(got) != 1 {
			t.Fatalf("builds = %v, want [good]", got)
		}
	})

	t.Run("project without a reports dir returns an empty array", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "demo"), 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		s := NewServer(dir, nil, RuntimeConfig{}, Versions{})

		w := callWithPath(s.getProject, http.MethodGet, "/projects/demo", nil, map[string]string{"id": "demo"})

		if got := builds(t, w); len(got) != 0 {
			t.Fatalf("builds = %v, want empty", got)
		}
		if body := strings.TrimSpace(w.Body.String()); body != `{"builds":[]}` {
			t.Errorf("body = %s, want an empty JSON array", body)
		}
	})

	t.Run("unknown project is not found", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.getProject, http.MethodGet, "/projects/nosuch", nil, map[string]string{"id": "nosuch"})

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
		}
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.getProject, http.MethodGet, "/projects/BADID", nil, map[string]string{"id": "BADID"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})
}

func TestServeProjectReport(t *testing.T) {
	t.Run("serves a report file", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())
		if err := os.WriteFile(filepath.Join(projects.ReportsDir(dir, "demo"), "latest", "app.js"), []byte("var x=1"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/app.js", nil,
			map[string]string{"id": "demo", "path": "latest/app.js"})

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}
		if got := w.Body.String(); got != "var x=1" {
			t.Errorf("body = %q, want the file content", got)
		}
	})

	t.Run("directory path serves its index.html", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/", nil,
			map[string]string{"id": "demo", "path": "latest/"})

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}
		if got := w.Body.String(); got != "<h1>latest</h1>" {
			t.Errorf("body = %q, want the index.html content", got)
		}
	})

	t.Run("explicit index.html redirects to the directory", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/index.html", nil,
			map[string]string{"id": "demo", "path": "latest/index.html"})

		if w.Code != http.StatusMovedPermanently {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusMovedPermanently)
		}
		if got := w.Header().Get("Location"); got != "./" {
			t.Errorf("Location = %q, want %q", got, "./")
		}
	})

	t.Run("history link redirects to its report directory", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "7", time.Now())

		target := "/projects/demo/reports/7/index.html/awesome"
		w := callWithPath(s.serveProjectReport, http.MethodGet, target, nil,
			map[string]string{"id": "demo", "path": "7/index.html/awesome"})

		if w.Code != http.StatusFound {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusFound)
		}

		if w.Body.Len() != 0 {
			t.Errorf("body = %q, want empty", w.Body)
		}
		loc, err := url.Parse(w.Header().Get("Location"))
		if err != nil {
			t.Fatalf("parsing Location: %v", err)
		}
		if loc.IsAbs() || strings.HasPrefix(loc.Path, "/") {
			t.Errorf("Location = %q, want a relative one that keeps a proxy's path prefix", loc)
		}
		base, _ := url.Parse(target)
		if got := base.ResolveReference(loc).Path; got != "/projects/demo/reports/7/" {
			t.Errorf("Location resolves to %q, want %q", got, "/projects/demo/reports/7/")
		}
	})

	t.Run("history link lands on the report behind a path prefix", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "7", time.Now())

		srv := httptest.NewServer(http.StripPrefix("/allure", s.Routes()))
		t.Cleanup(srv.Close)

		resp, err := srv.Client().Get(srv.URL + "/allure/projects/demo/reports/7/index.html/awesome")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", resp.StatusCode, http.StatusOK, body)
		}
		if got := resp.Request.URL.Path; got != "/allure/projects/demo/reports/7/" {
			t.Errorf("landed on %q, want %q", got, "/allure/projects/demo/reports/7/")
		}
		if string(body) != "<h1>7</h1>" {
			t.Errorf("body = %q, want the report's index.html", body)
		}
	})

	t.Run("only the history link shape is redirected", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "7", time.Now())

		writeBuild(t, dir, "demo", "7/awesome", time.Now())

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/7/awesome/", nil,
			map[string]string{"id": "demo", "path": "7/awesome/"})
		if w.Code != http.StatusOK {
			t.Fatalf("real awesome dir: status = %d, want %d", w.Code, http.StatusOK)
		}
		if got := w.Body.String(); got != "<h1>7/awesome</h1>" {
			t.Errorf("real awesome dir: body = %q, want its index.html", got)
		}

		for _, p := range []string{"7/myindex.html/awesome", "7/index.html/awesome/app.js"} {
			w = callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/"+p, nil,
				map[string]string{"id": "demo", "path": p})
			if w.Code != http.StatusNotFound {
				t.Errorf("%s: status = %d, want %d", p, w.Code, http.StatusNotFound)
			}
		}
	})

	t.Run("empty path is rejected", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/", nil,
			map[string]string{"id": "demo", "path": ""})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/BADID/reports/index.html", nil,
			map[string]string{"id": "BADID", "path": "index.html"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("missing file is not found", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/nope.html", nil,
			map[string]string{"id": "demo", "path": "latest/nope.html"})

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
		}
	})

	t.Run("a directory without an index.html is not listed", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())

		data := filepath.Join(projects.ReportsDir(dir, "demo"), "latest", "data")
		if err := os.MkdirAll(data, 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(data, "secret-attachment.txt"), []byte("private"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/data/", nil,
			map[string]string{"id": "demo", "path": "latest/data/"})

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body)
		}
		if strings.Contains(w.Body.String(), "secret-attachment.txt") {
			t.Errorf("response listed the directory's contents: %s", w.Body)
		}
	})

	t.Run("served files must be revalidated before reuse", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())

		for _, tc := range []struct{ name, path string }{
			{"index through the directory", "latest/"},
			{"a file by name", "latest/index.html"},
		} {
			w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/"+tc.path, nil,
				map[string]string{"id": "demo", "path": tc.path})

			if got := w.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("%s: Cache-Control = %q, want %q", tc.name, got, "no-cache")
			}
		}
	})

	t.Run("does not serve files outside the reports dir", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		secret := filepath.Join(dir, "secret.txt")
		if err := os.WriteFile(secret, []byte("top secret"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/../../secret.txt", nil,
			map[string]string{"id": "demo", "path": "../../secret.txt"})

		if w.Code == http.StatusOK {
			t.Fatalf("status = 200, want a refusal (body: %s)", w.Body)
		}
		if strings.Contains(w.Body.String(), "top secret") {
			t.Errorf("response leaked the file content: %s", w.Body)
		}
	})
}

func TestLockWaitingHandlersLiftTheWriteDeadline(t *testing.T) {

	cases := []struct {
		name    string
		method  string
		path    string
		handler func(*Server) http.HandlerFunc
	}{
		{"clearResults", http.MethodDelete, "/projects/demo/results", func(s *Server) http.HandlerFunc { return s.clearResults }},
		{"clearHistory", http.MethodPost, "/projects/demo/history/clean", func(s *Server) http.HandlerFunc { return s.clearHistory }},
		{"deleteProject", http.MethodDelete, "/projects/demo", func(s *Server) http.HandlerFunc { return s.deleteProject }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, "demo")

			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.SetPathValue("id", "demo")

			before := time.Now()
			rec := newDeadlineRecorder()
			tc.handler(s)(rec, r)

			if len(rec.writeDeadlines) != 1 {
				t.Fatalf("got %d write deadlines, want exactly 1", len(rec.writeDeadlines))
			}
			if got, want := rec.writeDeadlines[0], before.Add(lockWaitDeadline); got.Before(want) {
				t.Errorf("write deadline = %v, want at least %v (lockWaitDeadline out from the start)", got, want)
			}
		})
	}
}

func seedRequest(s *Server, target, body string) *httptest.ResponseRecorder {
	return callWithPath(s.seedHistory, http.MethodPost,
		"/projects/"+target+"/history/seed", strings.NewReader(body),
		map[string]string{"id": target})
}

func writeHistory(t *testing.T, dir, id, content string) {
	t.Helper()

	if err := os.WriteFile(projects.HistoryFile(dir, id), []byte(content), 0644); err != nil {
		t.Fatalf("setup history for %q: %v", id, err)
	}
}

func TestSeedHistory(t *testing.T) {
	const history = "{\"run\":1}\n{\"run\":2}\n"

	t.Run("copies the source history and answers 204", func(t *testing.T) {
		s, dir := newTestServer(t, "master", "mr-1")
		writeHistory(t, dir, "master", history)

		w := seedRequest(s, "mr-1", `{"from_project_id":"master"}`)

		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNoContent, w.Body)
		}

		if w.Body.Len() != 0 {
			t.Errorf("204 carried a body: %q", w.Body)
		}

		got, err := os.ReadFile(projects.HistoryFile(dir, "mr-1"))
		if err != nil {
			t.Fatalf("target has no history after a 204: %v", err)
		}
		if string(got) != history {
			t.Errorf("target history = %q, want %q", got, history)
		}
	})

	t.Run("rejects a malformed body", func(t *testing.T) {
		s, _ := newTestServer(t, "master", "mr-1")

		for _, body := range []string{"", "not json", `{"from_project_id":`} {
			w := seedRequest(s, "mr-1", body)

			if w.Code != http.StatusBadRequest {
				t.Errorf("body %q: status = %d, want %d (body: %s)", body, w.Code, http.StatusBadRequest, w.Body)
			}
			if got := w.Body.String(); got != "invalid request body\n" {
				t.Errorf("body %q: answered %q, want the decode error alone", body, got)
			}
		}
	})

	t.Run("rejects a source ID that escapes the projects root", func(t *testing.T) {
		s, dir := newTestServer(t, "mr-1")
		outside := filepath.Join(dir, "..", "outside.jsonl")
		if err := os.WriteFile(outside, []byte("secrets\n"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		w := seedRequest(s, "mr-1", `{"from_project_id":"../outside"}`)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
		if _, err := os.Stat(projects.HistoryFile(dir, "mr-1")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the escaping source was read anyway (stat err = %v)", err)
		}
	})

	t.Run("rejects an invalid target ID", func(t *testing.T) {
		s, _ := newTestServer(t, "master")

		w := seedRequest(s, "BAD_ID", `{"from_project_id":"master"}`)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
	})

	t.Run("reports a missing target project as 404", func(t *testing.T) {
		s, dir := newTestServer(t, "master")
		writeHistory(t, dir, "master", history)

		w := seedRequest(s, "mr-1", `{"from_project_id":"master"}`)

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body)
		}
	})

	t.Run("reports a source without history as 409", func(t *testing.T) {
		s, dir := newTestServer(t, "master", "mr-1")
		writeHistory(t, dir, "mr-1", "{\"stale\":true}\n")

		w := seedRequest(s, "mr-1", `{"from_project_id":"master"}`)

		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusConflict, w.Body)
		}

		if _, err := os.Stat(projects.HistoryFile(dir, "mr-1")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale target history survived the 409 (stat err = %v)", err)
		}
	})

	t.Run("reports a missing source project as 409", func(t *testing.T) {
		s, _ := newTestServer(t, "mr-1")

		w := seedRequest(s, "mr-1", `{"from_project_id":"nosuch"}`)

		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusConflict, w.Body)
		}
	})

	t.Run("rejects a source equal to the target", func(t *testing.T) {
		s, dir := newTestServer(t, "mr-1")
		writeHistory(t, dir, "mr-1", history)

		w := seedRequest(s, "mr-1", `{"from_project_id":"mr-1"}`)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
	})

	t.Run("keeps the cause of a 500 out of the response", func(t *testing.T) {
		s, dir := newTestServer(t, "master", "mr-1")
		writeHistory(t, dir, "master", history)

		if err := os.MkdirAll(filepath.Join(projects.HistoryFile(dir, "mr-1"), "occupied"), 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}

		w := seedRequest(s, "mr-1", `{"from_project_id":"master"}`)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusInternalServerError, w.Body)
		}
		if got := w.Body.String(); got != msgInternalError+"\n" {
			t.Errorf("500 answered %q, want %q - the cause belongs in the log", got, msgInternalError)
		}
	})
}
