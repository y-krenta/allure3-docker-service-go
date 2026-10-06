package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestRecoverer(t *testing.T) {
	t.Run("turns a panic into 500", func(t *testing.T) {
		h := recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}))

		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("leaves a healthy handler alone", func(t *testing.T) {
		h := recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}))

		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

		assert.Equal(t, http.StatusTeapot, w.Code)
	})
}

func TestRequestID(t *testing.T) {
	var seen string

	h := requestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = r.Context().Value(requestIDKey).(string)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	header := w.Header().Get("X-Request-ID")
	require.NotEmpty(t, header)
	assert.Equal(t, header, seen)
}

func TestStatusRecorder(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	rec.WriteHeader(http.StatusTeapot)

	assert.Equal(t, http.StatusTeapot, rec.status)
	assert.Equal(t, http.StatusTeapot, w.Code)
}

// http.MaxBytesReader finds the connection's writer by a bare type assertion,
// not an Unwrap walk, so the middleware has to hand it over for an oversized
// upload to get a clean 413.
func TestUnwrapResponseWriter(t *testing.T) {
	inner := httptest.NewRecorder()
	wrapped := &statusRecorder{ResponseWriter: &statusRecorder{ResponseWriter: inner}}

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

// TestRoutesListAllowedMethodsOn405 checks that a 405 names every method the
// path does accept, as RFC 9110 requires. Routers may send Allow as one
// comma-separated line or as several lines; both mean the same.
func TestRoutesListAllowedMethodsOn405(t *testing.T) {
	s, _ := newTestServer(t, "demo")

	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/projects", nil))

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)

	var allowed []string
	for _, line := range w.Header().Values("Allow") {
		for m := range strings.SplitSeq(line, ",") {
			allowed = append(allowed, strings.TrimSpace(m))
		}
	}
	assert.Contains(t, allowed, http.MethodGet)
	assert.Contains(t, allowed, http.MethodPost)
}
