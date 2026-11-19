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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

func writeBuild(t *testing.T, dir, id, build string, modTime time.Time) string {
	t.Helper()

	buildDir := filepath.Join(projects.ReportsDir(dir, id), build)
	require.NoError(t, os.MkdirAll(buildDir, 0755))

	index := filepath.Join(buildDir, "index.html")
	require.NoError(t, os.WriteFile(index, []byte("<h1>"+build+"</h1>"), 0644))
	require.NoError(t, os.Chtimes(index, modTime, modTime))

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

		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		assert.DirExists(t, projects.ResultsDir(dir, "demo"))
	})

	t.Run("rejects a malformed body", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.createProject, http.MethodPost, "/projects", strings.NewReader(`{"project_id":`), nil)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.createProject, http.MethodPost, "/projects", strings.NewReader(`{"project_id":"BAD ID!"}`), nil)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("existing project conflicts", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")

		w := callWithPath(s.createProject, http.MethodPost, "/projects", strings.NewReader(`{"project_id":"demo"}`), nil)

		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

func TestListProjects(t *testing.T) {
	decode := func(t *testing.T, w *httptest.ResponseRecorder) listProjectsResponse {
		t.Helper()

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var got listProjectsResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())

		return got
	}

	t.Run("empty dir returns an empty array, not null", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.listProjects, http.MethodGet, "/projects", nil, nil)

		require.Empty(t, decode(t, w).Projects)
		assert.JSONEq(t, `{"projects":[]}`, w.Body.String())
	})

	t.Run("lists directories only", func(t *testing.T) {
		s, dir := newTestServer(t, "alpha", "beta")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.txt"), nil, 0644))

		w := callWithPath(s.listProjects, http.MethodGet, "/projects", nil, nil)

		assert.ElementsMatch(t, []string{"alpha", "beta"}, decode(t, w).Projects)
	})

	t.Run("search matches case-insensitively", func(t *testing.T) {
		s, _ := newTestServer(t, "alpha", "beta", "alphabet")

		w := callWithPath(s.listProjects, http.MethodGet, "/projects?search=ALPHA", nil, nil)

		assert.ElementsMatch(t, []string{"alpha", "alphabet"}, decode(t, w).Projects)
	})

	t.Run("unreadable projects dir is a server error", func(t *testing.T) {
		s := NewServer(filepath.Join(t.TempDir(), "does-not-exist"), nil, RuntimeConfig{}, Versions{})

		w := callWithPath(s.listProjects, http.MethodGet, "/projects", nil, nil)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

func TestDeleteProject(t *testing.T) {
	t.Run("removes the project", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/demo", nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		_, err := os.Stat(filepath.Join(dir, "demo"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("default project is protected", func(t *testing.T) {
		s, dir := newTestServer(t, projects.DefaultProjectID)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/default", nil, map[string]string{"id": projects.DefaultProjectID})

		require.Equal(t, http.StatusForbidden, w.Code)
		assert.DirExists(t, filepath.Join(dir, projects.DefaultProjectID))
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/BADID", nil, map[string]string{"id": "BADID"})

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("deleting an unknown project succeeds", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/nosuch", nil, map[string]string{"id": "nosuch"})

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("removes through the generator, not behind its back", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/demo", nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		assert.Equal(t, []string{"demo"}, gen.deletedWith)
	})

	t.Run("a failed removal answers 500", func(t *testing.T) {
		gen := &stubGenerator{deleteErr: errors.New("boom")}
		s := newStubServer(gen)

		w := callWithPath(s.deleteProject, http.MethodDelete, "/projects/demo", nil, map[string]string{"id": "demo"})

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

func TestClearResults(t *testing.T) {
	t.Run("clears and answers 204 with no body", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.clearResults, http.MethodDelete, "/projects/demo/results",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		assert.Empty(t, w.Body.String())
		assert.Equal(t, []string{"demo"}, gen.clearedWith)
	})

	t.Run("unknown project is not found", func(t *testing.T) {
		s := newStubServer(&stubGenerator{
			clearErr: fmt.Errorf("%w: demo", report.ErrProjectNotFound),
		})

		w := callWithPath(s.clearResults, http.MethodDelete, "/projects/demo/results",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), msgInternalError, "a return is missing after the 404 write")
	})

	t.Run("a server error keeps its cause to itself", func(t *testing.T) {
		s := newStubServer(&stubGenerator{
			clearErr: errors.New("removing /app/projects/demo/results/x.json: permission denied"),
		})

		w := callWithPath(s.clearResults, http.MethodDelete, "/projects/demo/results",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), "/app/projects")
	})

	t.Run("a malformed id never reaches the generator", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.clearResults, http.MethodDelete, "/projects/BAD_ID/results",
			nil, map[string]string{"id": "BAD_ID"})

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Empty(t, gen.clearedWith)
	})
}

func TestGetProject(t *testing.T) {
	builds := func(t *testing.T, w *httptest.ResponseRecorder) []string {
		t.Helper()

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var got projectBuildsResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())

		return got.Builds
	}

	t.Run("sorts by index.html mtime with latest pinned first", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		now := time.Now()
		writeBuild(t, dir, "demo", "1", now.Add(-3*time.Hour))
		writeBuild(t, dir, "demo", "2", now.Add(-1*time.Hour))
		writeBuild(t, dir, "demo", "latest", now.Add(-2*time.Hour))

		w := callWithPath(s.getProject, http.MethodGet, "/projects/demo", nil, map[string]string{"id": "demo"})

		assert.Equal(t, []string{"latest", "2", "1"}, builds(t, w))
	})

	t.Run("skips build dirs without index.html", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "good", time.Now())
		require.NoError(t, os.MkdirAll(filepath.Join(projects.ReportsDir(dir, "demo"), "empty"), 0755))

		w := callWithPath(s.getProject, http.MethodGet, "/projects/demo", nil, map[string]string{"id": "demo"})

		assert.Equal(t, []string{"good"}, builds(t, w))
	})

	t.Run("skips files sitting next to build dirs", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "good", time.Now())
		require.NoError(t, os.WriteFile(filepath.Join(projects.ReportsDir(dir, "demo"), "stray.txt"), nil, 0644))

		w := callWithPath(s.getProject, http.MethodGet, "/projects/demo", nil, map[string]string{"id": "demo"})

		assert.Equal(t, []string{"good"}, builds(t, w))
	})

	t.Run("project without a reports dir returns an empty array", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "demo"), 0755))
		s := NewServer(dir, nil, RuntimeConfig{}, Versions{})

		w := callWithPath(s.getProject, http.MethodGet, "/projects/demo", nil, map[string]string{"id": "demo"})

		require.Empty(t, builds(t, w))
		assert.JSONEq(t, `{"builds":[]}`, w.Body.String())
	})

	t.Run("unknown project is not found", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.getProject, http.MethodGet, "/projects/nosuch", nil, map[string]string{"id": "nosuch"})

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.getProject, http.MethodGet, "/projects/BADID", nil, map[string]string{"id": "BADID"})

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestServeProjectReport(t *testing.T) {
	t.Run("serves a report file", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())
		require.NoError(t, os.WriteFile(filepath.Join(projects.ReportsDir(dir, "demo"), "latest", "app.js"), []byte("var x=1"), 0644))

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/app.js", nil,
			map[string]string{"id": "demo", "*": "latest/app.js"})

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "var x=1", w.Body.String())
	})

	t.Run("directory path serves its index.html", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/", nil,
			map[string]string{"id": "demo", "*": "latest/"})

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "<h1>latest</h1>", w.Body.String())
	})

	t.Run("explicit index.html redirects to the directory", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/index.html", nil,
			map[string]string{"id": "demo", "*": "latest/index.html"})

		require.Equal(t, http.StatusMovedPermanently, w.Code)
		assert.Equal(t, "./", w.Header().Get("Location"))
	})

	t.Run("history link redirects to its report directory", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "7", time.Now())

		target := "/projects/demo/reports/7/index.html/awesome"
		w := callWithPath(s.serveProjectReport, http.MethodGet, target, nil,
			map[string]string{"id": "demo", "*": "7/index.html/awesome"})

		require.Equal(t, http.StatusFound, w.Code)
		assert.Empty(t, w.Body.String())

		loc, err := url.Parse(w.Header().Get("Location"))
		require.NoError(t, err)
		assert.False(t, loc.IsAbs() || strings.HasPrefix(loc.Path, "/"),
			"Location = %q, want a relative one that keeps a proxy's path prefix", loc)

		base, _ := url.Parse(target)
		assert.Equal(t, "/projects/demo/reports/7/", base.ResolveReference(loc).Path)
	})

	t.Run("history link lands on the report behind a path prefix", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "7", time.Now())

		srv := httptest.NewServer(http.StripPrefix("/allure", s.Routes()))
		t.Cleanup(srv.Close)

		resp, err := srv.Client().Get(srv.URL + "/allure/projects/demo/reports/7/index.html/awesome")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		assert.Equal(t, "/allure/projects/demo/reports/7/", resp.Request.URL.Path)
		assert.Equal(t, "<h1>7</h1>", string(body))
	})

	t.Run("only the history link shape is redirected", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "7", time.Now())

		writeBuild(t, dir, "demo", "7/awesome", time.Now())

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/7/awesome/", nil,
			map[string]string{"id": "demo", "*": "7/awesome/"})
		require.Equal(t, http.StatusOK, w.Code, "real awesome dir")
		assert.Equal(t, "<h1>7/awesome</h1>", w.Body.String(), "real awesome dir")

		for _, p := range []string{"7/myindex.html/awesome", "7/index.html/awesome/app.js"} {
			w = callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/"+p, nil,
				map[string]string{"id": "demo", "*": p})
			assert.Equal(t, http.StatusNotFound, w.Code, p)
		}
	})

	t.Run("empty path is rejected", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/", nil,
			map[string]string{"id": "demo", "*": ""})

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/BADID/reports/index.html", nil,
			map[string]string{"id": "BADID", "*": "index.html"})

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing file is not found", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/nope.html", nil,
			map[string]string{"id": "demo", "*": "latest/nope.html"})

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("a directory without an index.html is not listed", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())

		data := filepath.Join(projects.ReportsDir(dir, "demo"), "latest", "data")
		require.NoError(t, os.MkdirAll(data, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(data, "secret-attachment.txt"), []byte("private"), 0644))

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/latest/data/", nil,
			map[string]string{"id": "demo", "*": "latest/data/"})

		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), "secret-attachment.txt")
	})

	t.Run("served files must be revalidated before reuse", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		writeBuild(t, dir, "demo", "latest", time.Now())

		for _, tc := range []struct{ name, path string }{
			{"index through the directory", "latest/"},
			{"a file by name", "latest/index.html"},
		} {
			w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/"+tc.path, nil,
				map[string]string{"id": "demo", "*": tc.path})

			assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"), tc.name)
		}
	})

	t.Run("does not serve files outside the reports dir", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("top secret"), 0644))

		w := callWithPath(s.serveProjectReport, http.MethodGet, "/projects/demo/reports/../../secret.txt", nil,
			map[string]string{"id": "demo", "*": "../../secret.txt"})

		require.NotEqual(t, http.StatusOK, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), "top secret")
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

			require.Len(t, rec.writeDeadlines, 1)
			assert.False(t, rec.writeDeadlines[0].Before(before.Add(lockWaitDeadline)),
				"write deadline = %v, want lockWaitDeadline out from the start", rec.writeDeadlines[0])
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

	require.NoError(t, os.WriteFile(projects.HistoryFile(dir, id), []byte(content), 0644))
}

func TestSeedHistory(t *testing.T) {
	const history = "{\"run\":1}\n{\"run\":2}\n"

	t.Run("copies the source history and answers 204", func(t *testing.T) {
		s, dir := newTestServer(t, "master", "mr-1")
		writeHistory(t, dir, "master", history)

		w := seedRequest(s, "mr-1", `{"from_project_id":"master"}`)

		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		assert.Empty(t, w.Body.String())

		got, err := os.ReadFile(projects.HistoryFile(dir, "mr-1"))
		require.NoError(t, err)
		assert.Equal(t, history, string(got))
	})

	t.Run("rejects a malformed body", func(t *testing.T) {
		s, _ := newTestServer(t, "master", "mr-1")

		for _, body := range []string{"", "not json", `{"from_project_id":`} {
			w := seedRequest(s, "mr-1", body)

			assert.Equal(t, http.StatusBadRequest, w.Code, "body %q", body)
			assert.Equal(t, "invalid request body\n", w.Body.String(), "body %q", body)
		}
	})

	t.Run("rejects a source ID that escapes the projects root", func(t *testing.T) {
		s, dir := newTestServer(t, "mr-1")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "..", "outside.jsonl"), []byte("secrets\n"), 0644))

		w := seedRequest(s, "mr-1", `{"from_project_id":"../outside"}`)

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		_, err := os.Stat(projects.HistoryFile(dir, "mr-1"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("rejects an invalid target ID", func(t *testing.T) {
		s, _ := newTestServer(t, "master")

		w := seedRequest(s, "BAD_ID", `{"from_project_id":"master"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	})

	t.Run("reports a missing target project as 404", func(t *testing.T) {
		s, dir := newTestServer(t, "master")
		writeHistory(t, dir, "master", history)

		w := seedRequest(s, "mr-1", `{"from_project_id":"master"}`)

		assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	})

	t.Run("reports a source without history as 409", func(t *testing.T) {
		s, dir := newTestServer(t, "master", "mr-1")
		writeHistory(t, dir, "mr-1", "{\"stale\":true}\n")

		w := seedRequest(s, "mr-1", `{"from_project_id":"master"}`)

		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		_, err := os.Stat(projects.HistoryFile(dir, "mr-1"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("reports a missing source project as 409", func(t *testing.T) {
		s, _ := newTestServer(t, "mr-1")

		w := seedRequest(s, "mr-1", `{"from_project_id":"nosuch"}`)

		assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	})

	t.Run("rejects a source equal to the target", func(t *testing.T) {
		s, dir := newTestServer(t, "mr-1")
		writeHistory(t, dir, "mr-1", history)

		w := seedRequest(s, "mr-1", `{"from_project_id":"mr-1"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	})

	t.Run("keeps the cause of a 500 out of the response", func(t *testing.T) {
		s, dir := newTestServer(t, "master", "mr-1")
		writeHistory(t, dir, "master", history)

		require.NoError(t, os.MkdirAll(filepath.Join(projects.HistoryFile(dir, "mr-1"), "occupied"), 0755))

		w := seedRequest(s, "mr-1", `{"from_project_id":"master"}`)

		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		assert.Equal(t, msgInternalError+"\n", w.Body.String(), "the cause belongs in the log")
	})
}
