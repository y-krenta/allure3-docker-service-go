package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

type stubGenerator struct {
	startErr        error
	clearErr        error
	clearHistoryErr error
	exportErr       error
	deleteErr       error

	status    report.Status
	hasStatus bool

	exportBody string

	startedWith        []string
	clearedWith        []string
	clearedHistoryWith []string
	exportedWith       []string
	deletedWith        []string
}

func (g *stubGenerator) Start(_ context.Context, projectID string) error {
	g.startedWith = append(g.startedWith, projectID)
	return g.startErr
}

func (g *stubGenerator) Status(string) (report.Status, bool) {
	return g.status, g.hasStatus
}

func (g *stubGenerator) ClearResults(projectID string) error {
	g.clearedWith = append(g.clearedWith, projectID)
	return g.clearErr
}

func (g *stubGenerator) ClearHistory(_ context.Context, projectID string) error {
	g.clearedHistoryWith = append(g.clearedHistoryWith, projectID)
	return g.clearHistoryErr
}

func (g *stubGenerator) Delete(projectID string) error {
	g.deletedWith = append(g.deletedWith, projectID)
	return g.deleteErr
}

func (g *stubGenerator) ExportLatest(projectID string, w io.Writer) error {
	g.exportedWith = append(g.exportedWith, projectID)
	if g.exportBody != "" {
		if _, err := io.WriteString(w, g.exportBody); err != nil {
			return err
		}
	}
	return g.exportErr
}

func newStubServer(gen *stubGenerator) *Server {
	return NewServer("unused-dir", gen, RuntimeConfig{}, Versions{})
}

func TestStartGeneration(t *testing.T) {
	t.Run("accepted build answers 202 with no body", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.startGeneration, http.MethodPost, "/projects/demo/generation",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusAccepted, w.Body)
		}
		if got := w.Body.String(); got != "" {
			t.Errorf("body = %q, want empty", got)
		}
		if len(gen.startedWith) != 1 || gen.startedWith[0] != "demo" {
			t.Errorf("Start called with %v, want exactly one call for %q", gen.startedWith, "demo")
		}
	})

	t.Run("error mapping", func(t *testing.T) {
		tests := []struct {
			name     string
			startErr error
			want     int
		}{
			{"unknown project", fmt.Errorf("%w: demo", report.ErrProjectNotFound), http.StatusNotFound},
			{"build already running", fmt.Errorf("%w: demo", report.ErrAlreadyRunning), http.StatusConflict},
			{"nothing to build from", fmt.Errorf("%w: demo", report.ErrNoResults), http.StatusConflict},
			{"anything else", errors.New("disk on fire"), http.StatusInternalServerError},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				s := newStubServer(&stubGenerator{startErr: tt.startErr})

				w := callWithPath(s.startGeneration, http.MethodPost, "/projects/demo/generation",
					nil, map[string]string{"id": "demo"})

				if w.Code != tt.want {
					t.Fatalf("status = %d, want %d (body: %s)", w.Code, tt.want, w.Body)
				}
			})
		}
	})

	t.Run("the two conflicts are told apart in the body", func(t *testing.T) {
		bodies := make(map[string]string)
		for _, sentinel := range []error{report.ErrAlreadyRunning, report.ErrNoResults} {
			s := newStubServer(&stubGenerator{startErr: fmt.Errorf("%w: demo", sentinel)})

			w := callWithPath(s.startGeneration, http.MethodPost, "/projects/demo/generation",
				nil, map[string]string{"id": "demo"})

			bodies[sentinel.Error()] = w.Body.String()
		}

		running, noResults := bodies[report.ErrAlreadyRunning.Error()], bodies[report.ErrNoResults.Error()]
		if running == noResults {
			t.Fatalf("both 409s answer %q; a caller cannot tell a running build from empty results", running)
		}
	})

	t.Run("a server error keeps its cause to itself", func(t *testing.T) {
		s := newStubServer(&stubGenerator{
			startErr: errors.New("checking for results directory: open /app/projects/demo/results: permission denied"),
		})

		w := callWithPath(s.startGeneration, http.MethodPost, "/projects/demo/generation",
			nil, map[string]string{"id": "demo"})

		if body := w.Body.String(); strings.Contains(body, "/app/projects") {
			t.Errorf("body = %q, want no internal paths", body)
		}
	})

	t.Run("a malformed id never reaches the generator", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.startGeneration, http.MethodPost, "/projects/BAD_ID/generation",
			nil, map[string]string{"id": "BAD_ID"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
		if len(gen.startedWith) != 0 {
			t.Errorf("Start was called with %v, want no call at all", gen.startedWith)
		}
	})
}

func TestGenerationStatus(t *testing.T) {
	started := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)

	decode := func(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
		t.Helper()

		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding %q: %v", w.Body, err)
		}
		return got
	}

	call := func(gen *stubGenerator) *httptest.ResponseRecorder {
		return callWithPath(newStubServer(gen).generationStatus, http.MethodGet,
			"/projects/demo/generation", nil, map[string]string{"id": "demo"})
	}

	t.Run("a finished build reports both timestamps", func(t *testing.T) {
		w := call(&stubGenerator{
			hasStatus: true,
			status: report.Status{
				State:      report.StateSucceeded,
				StartedAt:  started,
				FinishedAt: finished,
			},
		})

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		got := decode(t, w)
		if got["state"] != "succeeded" {
			t.Errorf("state = %v, want succeeded", got["state"])
		}
		if got["started_at"] != started.Format(time.RFC3339Nano) {
			t.Errorf("started_at = %v, want %v", got["started_at"], started.Format(time.RFC3339Nano))
		}
		if got["finished_at"] != finished.Format(time.RFC3339Nano) {
			t.Errorf("finished_at = %v, want %v", got["finished_at"], finished.Format(time.RFC3339Nano))
		}
		if _, ok := got["error"]; ok {
			t.Errorf("error = %v, want the key absent on a successful build", got["error"])
		}
	})

	t.Run("a running build has no finished_at at all", func(t *testing.T) {
		w := call(&stubGenerator{
			hasStatus: true,
			status:    report.Status{State: report.StateRunning, StartedAt: started},
		})

		got := decode(t, w)
		if _, ok := got["finished_at"]; ok {

			t.Errorf("finished_at = %v, want the key absent while the build runs", got["finished_at"])
		}
	})

	t.Run("a failed build is still 200 and carries the reason", func(t *testing.T) {
		w := call(&stubGenerator{
			hasStatus: true,
			status: report.Status{
				State:      report.StateFailed,
				StartedAt:  started,
				FinishedAt: finished,
				Err:        errors.New("running allure: exit status 3, stderr: boom"),
			},
		})

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: reading the status succeeded, the build is what failed",
				w.Code, http.StatusOK)
		}

		got := decode(t, w)
		if got["state"] != "failed" {
			t.Errorf("state = %v, want failed", got["state"])
		}
		if got["error"] != "running allure: exit status 3, stderr: boom" {
			t.Errorf("error = %v, want the build's own message", got["error"])
		}
	})

	t.Run("a project nobody has generated is 404", func(t *testing.T) {
		w := call(&stubGenerator{hasStatus: false})

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body)
		}

		if body := w.Body.String(); strings.Contains(body, "{") {
			t.Errorf("body = %q, want the 404 message alone", body)
		}
	})

	t.Run("a malformed id is 400", func(t *testing.T) {
		w := callWithPath(newStubServer(&stubGenerator{}).generationStatus, http.MethodGet,
			"/projects/BAD_ID/generation", nil, map[string]string{"id": "BAD_ID"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
	})
}

func TestClearHistory(t *testing.T) {
	t.Run("accepted with no body", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.clearHistory, http.MethodPost, "/projects/demo/history/clean",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusAccepted, w.Body)
		}
		if got := w.Body.String(); got != "" {
			t.Errorf("body = %q, want empty", got)
		}
		if len(gen.clearedHistoryWith) != 1 || gen.clearedHistoryWith[0] != "demo" {
			t.Errorf("ClearHistory called with %v, want exactly one call for %q", gen.clearedHistoryWith, "demo")
		}
	})

	t.Run("error mapping", func(t *testing.T) {
		tests := []struct {
			name     string
			clearErr error
			want     int
		}{
			{"unknown project", fmt.Errorf("%w: demo", report.ErrProjectNotFound), http.StatusNotFound},
			{"build already running", fmt.Errorf("%w: demo", report.ErrAlreadyRunning), http.StatusConflict},
			{"nothing to build from", fmt.Errorf("%w: demo", report.ErrNoResults), http.StatusConflict},
			{"anything else", errors.New("disk on fire"), http.StatusInternalServerError},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				s := newStubServer(&stubGenerator{clearHistoryErr: tt.clearErr})

				w := callWithPath(s.clearHistory, http.MethodPost, "/projects/demo/history/clean",
					nil, map[string]string{"id": "demo"})

				if w.Code != tt.want {
					t.Fatalf("status = %d, want %d (body: %s)", w.Code, tt.want, w.Body)
				}
			})
		}
	})

	t.Run("the two conflicts are told apart in the body", func(t *testing.T) {
		bodies := make(map[string]string)
		for _, sentinel := range []error{report.ErrAlreadyRunning, report.ErrNoResults} {
			s := newStubServer(&stubGenerator{clearHistoryErr: fmt.Errorf("%w: demo", sentinel)})

			w := callWithPath(s.clearHistory, http.MethodPost, "/projects/demo/history/clean",
				nil, map[string]string{"id": "demo"})

			bodies[sentinel.Error()] = w.Body.String()
		}

		running, noResults := bodies[report.ErrAlreadyRunning.Error()], bodies[report.ErrNoResults.Error()]
		if running == noResults {
			t.Fatalf("both 409s answer %q; a caller cannot tell a running build from empty results", running)
		}
	})

	t.Run("a server error keeps its cause to itself", func(t *testing.T) {
		s := newStubServer(&stubGenerator{
			clearHistoryErr: errors.New("clearing history directory: open /app/projects/demo/reports: permission denied"),
		})

		w := callWithPath(s.clearHistory, http.MethodPost, "/projects/demo/history/clean",
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

		w := callWithPath(s.clearHistory, http.MethodPost, "/projects/BAD_ID/history/clean",
			nil, map[string]string{"id": "BAD_ID"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
		if len(gen.clearedHistoryWith) != 0 {
			t.Errorf("ClearHistory was called with %v, want no call at all", gen.clearedHistoryWith)
		}
	})

	t.Run("success is not mistaken for a server error", func(t *testing.T) {

		s := newStubServer(&stubGenerator{})

		w := callWithPath(s.clearHistory, http.MethodPost, "/projects/demo/history/clean",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusAccepted, w.Body)
		}
	})
}

func newExportServer(t *testing.T, gen *stubGenerator, withReport ...string) *Server {
	t.Helper()

	dir := t.TempDir()
	for _, id := range withReport {
		if err := projects.CreateDir(dir, id); err != nil {
			t.Fatalf("setup project %q: %v", id, err)
		}
		latest := projects.LatestReportDir(dir, id)
		if err := os.MkdirAll(latest, 0o755); err != nil {
			t.Fatalf("setup report for %q: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(latest, "index.html"), []byte("<html>"), 0o644); err != nil {
			t.Fatalf("setup report for %q: %v", id, err)
		}
	}
	return NewServer(dir, gen, RuntimeConfig{}, Versions{})
}

func TestExportReport(t *testing.T) {

	t.Run("serves the archive as an attachment", func(t *testing.T) {
		gen := &stubGenerator{exportBody: "pretend archive bytes"}
		s := newExportServer(t, gen, "demo")

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/demo/report/export",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}
		if got := w.Header().Get("Content-Type"); got != "application/zip" {
			t.Errorf("Content-Type = %q, want %q", got, "application/zip")
		}
		if got := w.Body.String(); got != gen.exportBody {
			t.Errorf("body = %q, want the archive the generator wrote (%q)", got, gen.exportBody)
		}
		if len(gen.exportedWith) != 1 || gen.exportedWith[0] != "demo" {
			t.Errorf("ExportLatest called with %v, want exactly one call for %q", gen.exportedWith, "demo")
		}
	})

	t.Run("names the download after the project", func(t *testing.T) {
		gen := &stubGenerator{exportBody: "archive"}
		s := newExportServer(t, gen, "my project")

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/my%20project/report/export",
			nil, map[string]string{"id": "my project"})

		want := `attachment; filename="my project-report.zip"`
		if got := w.Header().Get("Content-Disposition"); got != want {
			t.Errorf("Content-Disposition = %q, want %q", got, want)
		}
	})

	t.Run("a project with no published report answers 404", func(t *testing.T) {
		gen := &stubGenerator{exportBody: "archive"}
		s := newExportServer(t, gen)

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/demo/report/export",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body)
		}

		if got := w.Body.String(); got != "report not found\n" {
			t.Errorf("body = %q, want only the 404 message", got)
		}
		if len(gen.exportedWith) != 0 {
			t.Errorf("ExportLatest was called with %v, want no call at all", gen.exportedWith)
		}
	})

	t.Run("an invalid project id answers 400", func(t *testing.T) {
		gen := &stubGenerator{exportBody: "archive"}
		s := newExportServer(t, gen)

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/Bad%20Id/report/export",
			nil, map[string]string{"id": "Bad Id"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
		if len(gen.exportedWith) != 0 {
			t.Errorf("ExportLatest was called with %v, want no call at all", gen.exportedWith)
		}
	})

	t.Run("extends the write deadline past the server default", func(t *testing.T) {
		rec := newDeadlineRecorder()
		r := httptest.NewRequest(http.MethodGet, "/projects/demo/report/export", nil)
		r.SetPathValue("id", "demo")

		before := time.Now()
		newExportServer(t, &stubGenerator{exportBody: "archive"}, "demo").exportReport(rec, r)

		if len(rec.writeDeadlines) != 1 {
			t.Fatalf("write deadlines set = %v, want exactly one", rec.writeDeadlines)
		}
		if got := rec.writeDeadlines[0].Sub(before); got < exportWriteDeadline {
			t.Errorf("write deadline set %v ahead, want at least %v", got, exportWriteDeadline)
		}
	})

	t.Run("a failure part-way through still reads as 200", func(t *testing.T) {
		gen := &stubGenerator{
			exportBody: "half an archive",
			exportErr:  errors.New("disk went away"),
		}
		s := newExportServer(t, gen, "demo")

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/demo/report/export",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if got := w.Body.String(); got != "half an archive" {
			t.Errorf("body = %q, want the bytes written before the failure", got)
		}
	})
}

func TestLatestReport(t *testing.T) {
	t.Run("redirects to the published report", func(t *testing.T) {
		s := newExportServer(t, &stubGenerator{}, "demo")

		w := callWithPath(s.latestReport, http.MethodGet, "/projects/demo/latest-report",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusFound {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusFound, w.Body)
		}
		want := "/projects/demo/reports/latest/"
		if got := w.Header().Get("Location"); got != want {
			t.Errorf("Location = %q, want %q", got, want)
		}
	})

	t.Run("a project with no published report answers 404", func(t *testing.T) {
		s := newExportServer(t, &stubGenerator{})

		w := callWithPath(s.latestReport, http.MethodGet, "/projects/demo/latest-report",
			nil, map[string]string{"id": "demo"})

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body)
		}
		if got := w.Body.String(); got != "latest report not found\n" {
			t.Errorf("body = %q, want only the 404 message", got)
		}
		if got := w.Header().Get("Location"); got != "" {
			t.Errorf("Location = %q, want no redirect header at all", got)
		}
	})

	t.Run("an invalid project id answers 400", func(t *testing.T) {
		s := newExportServer(t, &stubGenerator{})

		w := callWithPath(s.latestReport, http.MethodGet, "/projects/Bad%20Id/latest-report",
			nil, map[string]string{"id": "Bad Id"})

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
		if got := w.Header().Get("Location"); got != "" {
			t.Errorf("Location = %q, want no redirect header at all", got)
		}
	})
}
