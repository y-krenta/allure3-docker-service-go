package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

func TestRoutes(t *testing.T) {
	s, _ := newTestServer(t, "demo")

	s.reports = &stubGenerator{hasStatus: true, status: report.Status{State: report.StateRunning}}
	h := s.Routes()

	body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

	tests := []struct {
		name        string
		method      string
		target      string
		body        *strings.Reader
		contentType string
		wantStatus  int
	}{
		{name: "health", method: http.MethodGet, target: "/health", wantStatus: http.StatusOK},
		{name: "list projects", method: http.MethodGet, target: "/projects", wantStatus: http.StatusOK},
		{name: "get project", method: http.MethodGet, target: "/projects/demo", wantStatus: http.StatusOK},
		{name: "create project", method: http.MethodPost, target: "/projects",
			body: strings.NewReader(`{"project_id":"fresh"}`), contentType: "application/json", wantStatus: http.StatusCreated},
		{name: "delete project", method: http.MethodDelete, target: "/projects/fresh", wantStatus: http.StatusNoContent},
		{name: "serve report", method: http.MethodGet, target: "/projects/demo/reports/latest/app.js", wantStatus: http.StatusNotFound},
		{name: "start generation", method: http.MethodPost, target: "/projects/demo/generation", wantStatus: http.StatusAccepted},
		{name: "generation status", method: http.MethodGet, target: "/projects/demo/generation", wantStatus: http.StatusOK},

		{name: "seed history", method: http.MethodPost, target: "/projects/demo/history/seed",
			body: strings.NewReader(`{"from_project_id":"baseline"}`), contentType: "application/json", wantStatus: http.StatusConflict},

		{name: "unknown path", method: http.MethodGet, target: "/nope", wantStatus: http.StatusNotFound},
		{name: "wrong method on projects", method: http.MethodPut, target: "/projects", wantStatus: http.StatusMethodNotAllowed},
		{name: "wrong method on seed history", method: http.MethodGet, target: "/projects/demo/history/seed", wantStatus: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *http.Request
			if tt.body != nil {
				r = httptest.NewRequest(tt.method, tt.target, tt.body)
				r.Header.Set("Content-Type", tt.contentType)
			} else {
				r = httptest.NewRequest(tt.method, tt.target, nil)
			}

			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
		})
	}

	t.Run("send results reaches the upload handler", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/projects/demo/results", body)
		r.Header.Set("Content-Type", ct)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "a-result.json")
	})

	t.Run("every response carries a request id", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/health", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		assert.NotEmpty(t, w.Header().Get("X-Request-ID"))
	})
}

// captureLogs sends slog's default logger into a buffer for the rest of the
// test. Call it before s.Routes(), which takes the logger when it is built.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// logLinesWithRequestID returns the JSON log lines whose request_id is id.
func logLinesWithRequestID(t *testing.T, logs *bytes.Buffer, id string) []map[string]any {
	t.Helper()

	var lines []map[string]any
	for line := range strings.Lines(logs.String()) {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if entry["request_id"] == id {
			lines = append(lines, entry)
		}
	}
	return lines
}

// TestRoutesLogOneLinePerRequest checks that every request, a panicking one
// included, leaves exactly one log line carrying its X-Request-ID, and that a
// panic is answered with 500.
func TestRoutesLogOneLinePerRequest(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		reports    *stubGenerator
		wantStatus int
	}{
		{name: "regular request", method: http.MethodGet, target: "/health", wantStatus: http.StatusOK},
		{name: "panic", method: http.MethodPost, target: "/projects/demo/generation",
			reports: &stubGenerator{startPanic: "boom"}, wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			s, _ := newTestServer(t, "demo")
			if tt.reports != nil {
				s.reports = tt.reports
			}

			w := httptest.NewRecorder()
			s.Routes().ServeHTTP(w, httptest.NewRequest(tt.method, tt.target, nil))

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			id := w.Header().Get("X-Request-ID")
			require.NotEmpty(t, id)
			assert.Len(t, logLinesWithRequestID(t, logs, id), 1, logs.String())
		})
	}
}

// TestRequestID checks that every response carries its own UUIDv7 in
// X-Request-ID.
func TestRequestID(t *testing.T) {
	h := requestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	ids := make([]string, 2)
	for i := range ids {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		ids[i] = w.Header().Get("X-Request-ID")

		id, err := uuid.Parse(ids[i])
		require.NoError(t, err, ids[i])
		assert.Equal(t, uuid.Version(7), id.Version())
	}
	assert.NotEqual(t, ids[0], ids[1])
}

// http.MaxBytesReader finds the connection's writer by a bare type assertion,
// not an Unwrap walk, so the middleware has to hand it over for an oversized
// upload to get a clean 413.
func TestUnwrapResponseWriter(t *testing.T) {
	inner := httptest.NewRecorder()
	wrapped := middleware.NewWrapResponseWriter(middleware.NewWrapResponseWriter(inner, 1), 1)

	assert.Same(t, inner, unwrapResponseWriter(wrapped))
	assert.Same(t, inner, unwrapResponseWriter(inner))
}

// writeReportFile puts content at rel inside the project's published report.
func writeReportFile(t *testing.T, dir, id, rel, content string) {
	t.Helper()

	p := filepath.Join(projects.LatestReportDir(dir, id), filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

// TestRoutesAnswerHeadOnGetRoutes checks that GET routes also answer HEAD:
// probes and caches send HEAD, and a router that wants it registered
// separately answers them 405.
func TestRoutesAnswerHeadOnGetRoutes(t *testing.T) {
	s, dir := newTestServer(t, "demo")
	writeReportFile(t, dir, "demo", "data/summary.json", `{}`)
	h := s.Routes()

	for _, target := range []string{"/health", "//health", "/projects", "/projects/demo/reports/latest/data/summary.json"} {
		t.Run(target, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodHead, target, nil))

			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

// TestRoutesToleratePathsWithDoubleSlashes checks that a doubled slash still
// reaches its route. A CI script joining a base URL that ends in "/" with
// "/projects" sends "//projects"; it must keep working once redirects, if
// any, are followed - the way curl -L and http.Client follow them.
func TestRoutesToleratePathsWithDoubleSlashes(t *testing.T) {
	s, _ := newTestServer(t, "demo")
	s.reports = &stubGenerator{}
	srv := httptest.NewServer(s.Routes())
	t.Cleanup(srv.Close)

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "//projects", http.StatusOK},
		{http.MethodGet, "/projects//demo", http.StatusOK},
		{http.MethodPost, "//projects/demo/generation", http.StatusAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, srv.URL+tt.path, nil)
			require.NoError(t, err)

			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, tt.want, resp.StatusCode)
		})
	}
}

// TestRoutesServeReportFiles checks that the tail of a report URL reaches the
// handler intact, nested directories included, and the file comes back.
func TestRoutesServeReportFiles(t *testing.T) {
	s, dir := newTestServer(t, "demo")
	writeReportFile(t, dir, "demo", "data/test-results/abc.json", `{"name":"login"}`)

	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/projects/demo/reports/latest/data/test-results/abc.json", nil))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"name":"login"}`, w.Body.String())
}

// TestRoutesKeepReportPathsInsideReports checks that ".." in a report URL
// never serves a file from outside the project's reports directory.
func TestRoutesKeepReportPathsInsideReports(t *testing.T) {
	s, dir := newTestServer(t, "demo")
	writeReportFile(t, dir, "demo", "index.html", "<html>report</html>")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("top secret"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "demo", "secret.txt"), []byte("top secret"), 0o644))

	for _, target := range []string{
		"/projects/demo/reports/../secret.txt",
		"/projects/demo/reports/../../secret.txt",
		"/projects/demo/reports/latest/../../secret.txt",
		"/projects/demo/reports/latest/../../../secret.txt",
	} {
		t.Run(target, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))

			assert.NotEqual(t, http.StatusOK, w.Code)
			assert.NotContains(t, w.Body.String(), "top secret")
		})
	}
}
