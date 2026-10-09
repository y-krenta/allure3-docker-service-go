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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	startPanic any

	startedWith        []string
	clearedWith        []string
	clearedHistoryWith []string
	exportedWith       []string
	deletedWith        []string
}

func (g *stubGenerator) Start(_ context.Context, projectID string) error {
	g.startedWith = append(g.startedWith, projectID)
	if g.startPanic != nil {
		panic(g.startPanic)
	}
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

		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		assert.Empty(t, w.Body.String())
		assert.Equal(t, []string{"demo"}, gen.startedWith)
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

				require.Equal(t, tt.want, w.Code, w.Body.String())
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
		assert.NotEqual(t, running, noResults, "a caller cannot tell a running build from empty results")
	})

	t.Run("a server error keeps its cause to itself", func(t *testing.T) {
		s := newStubServer(&stubGenerator{
			startErr: errors.New("checking for results directory: open /app/projects/demo/results: permission denied"),
		})

		w := callWithPath(s.startGeneration, http.MethodPost, "/projects/demo/generation",
			nil, map[string]string{"id": "demo"})

		assert.NotContains(t, w.Body.String(), "/app/projects")
	})

	t.Run("a malformed id never reaches the generator", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.startGeneration, http.MethodPost, "/projects/BAD_ID/generation",
			nil, map[string]string{"id": "BAD_ID"})

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Empty(t, gen.startedWith)
	})
}

func TestGenerationStatus(t *testing.T) {
	started := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)

	decode := func(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
		t.Helper()

		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())
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

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		got := decode(t, w)
		assert.Equal(t, "succeeded", got["state"])
		assert.Equal(t, started.Format(time.RFC3339Nano), got["started_at"])
		assert.Equal(t, finished.Format(time.RFC3339Nano), got["finished_at"])
		assert.NotContains(t, got, "error")
	})

	t.Run("a running build has no finished_at at all", func(t *testing.T) {
		w := call(&stubGenerator{
			hasStatus: true,
			status:    report.Status{State: report.StateRunning, StartedAt: started},
		})

		assert.NotContains(t, decode(t, w), "finished_at")
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

		require.Equal(t, http.StatusOK, w.Code, "reading the status succeeded, the build is what failed")

		got := decode(t, w)
		assert.Equal(t, "failed", got["state"])
		assert.Equal(t, "running allure: exit status 3, stderr: boom", got["error"])
	})

	t.Run("a project nobody has generated is 404", func(t *testing.T) {
		w := call(&stubGenerator{hasStatus: false})

		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), "{", "want the 404 message alone")
	})

	t.Run("a malformed id is 400", func(t *testing.T) {
		w := callWithPath(newStubServer(&stubGenerator{}).generationStatus, http.MethodGet,
			"/projects/BAD_ID/generation", nil, map[string]string{"id": "BAD_ID"})

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	})
}

func TestClearHistory(t *testing.T) {
	t.Run("accepted with no body", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.clearHistory, http.MethodPost, "/projects/demo/history/clean",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		assert.Empty(t, w.Body.String())
		assert.Equal(t, []string{"demo"}, gen.clearedHistoryWith)
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

				require.Equal(t, tt.want, w.Code, w.Body.String())
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
		assert.NotEqual(t, running, noResults, "a caller cannot tell a running build from empty results")
	})

	t.Run("a server error keeps its cause to itself", func(t *testing.T) {
		s := newStubServer(&stubGenerator{
			clearHistoryErr: errors.New("clearing history directory: open /app/projects/demo/reports: permission denied"),
		})

		w := callWithPath(s.clearHistory, http.MethodPost, "/projects/demo/history/clean",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), "/app/projects")
	})

	t.Run("a malformed id never reaches the generator", func(t *testing.T) {
		gen := &stubGenerator{}
		s := newStubServer(gen)

		w := callWithPath(s.clearHistory, http.MethodPost, "/projects/BAD_ID/history/clean",
			nil, map[string]string{"id": "BAD_ID"})

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Empty(t, gen.clearedHistoryWith)
	})

	t.Run("success is not mistaken for a server error", func(t *testing.T) {

		s := newStubServer(&stubGenerator{})

		w := callWithPath(s.clearHistory, http.MethodPost, "/projects/demo/history/clean",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	})
}

func newExportServer(t *testing.T, gen *stubGenerator, withReport ...string) *Server {
	t.Helper()

	dir := t.TempDir()
	for _, id := range withReport {
		require.NoError(t, projects.CreateDir(dir, id))
		latest := projects.LatestReportDir(dir, id)
		require.NoError(t, os.MkdirAll(latest, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(latest, "index.html"), []byte("<html>"), 0o644))
	}
	return NewServer(dir, gen, RuntimeConfig{}, Versions{})
}

func TestExportReport(t *testing.T) {

	t.Run("serves the archive as an attachment", func(t *testing.T) {
		gen := &stubGenerator{exportBody: "pretend archive bytes"}
		s := newExportServer(t, gen, "demo")

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/demo/report/export",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))
		assert.Equal(t, gen.exportBody, w.Body.String())
		assert.Equal(t, []string{"demo"}, gen.exportedWith)
	})

	t.Run("names the download after the project", func(t *testing.T) {
		gen := &stubGenerator{exportBody: "archive"}
		s := newExportServer(t, gen, "my project")

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/my%20project/report/export",
			nil, map[string]string{"id": "my project"})

		assert.Equal(t, `attachment; filename="my project-report.zip"`, w.Header().Get("Content-Disposition"))
	})

	t.Run("a project with no published report answers 404", func(t *testing.T) {
		gen := &stubGenerator{exportBody: "archive"}
		s := newExportServer(t, gen)

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/demo/report/export",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		assert.Equal(t, "report not found\n", w.Body.String())
		assert.Empty(t, gen.exportedWith)
	})

	t.Run("an invalid project id answers 400", func(t *testing.T) {
		gen := &stubGenerator{exportBody: "archive"}
		s := newExportServer(t, gen)

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/Bad%20Id/report/export",
			nil, map[string]string{"id": "Bad Id"})

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Empty(t, gen.exportedWith)
	})

	t.Run("extends the write deadline past the server default", func(t *testing.T) {
		rec := newDeadlineRecorder()
		r := httptest.NewRequest(http.MethodGet, "/projects/demo/report/export", nil)
		r.SetPathValue("id", "demo")

		before := time.Now()
		newExportServer(t, &stubGenerator{exportBody: "archive"}, "demo").exportReport(rec, r)

		require.Len(t, rec.writeDeadlines, 1)
		assert.GreaterOrEqual(t, rec.writeDeadlines[0].Sub(before), exportWriteDeadline)
	})

	t.Run("a failure part-way through still reads as 200", func(t *testing.T) {
		gen := &stubGenerator{
			exportBody: "half an archive",
			exportErr:  errors.New("disk went away"),
		}
		s := newExportServer(t, gen, "demo")

		w := callWithPath(s.exportReport, http.MethodGet, "/projects/demo/report/export",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "half an archive", w.Body.String())
	})
}

func TestLatestReport(t *testing.T) {
	t.Run("redirects to the published report", func(t *testing.T) {
		s := newExportServer(t, &stubGenerator{}, "demo")

		w := callWithPath(s.latestReport, http.MethodGet, "/projects/demo/latest-report",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusFound, w.Code, w.Body.String())
		assert.Equal(t, "/projects/demo/reports/latest/", w.Header().Get("Location"))
	})

	t.Run("a project with no published report answers 404", func(t *testing.T) {
		s := newExportServer(t, &stubGenerator{})

		w := callWithPath(s.latestReport, http.MethodGet, "/projects/demo/latest-report",
			nil, map[string]string{"id": "demo"})

		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		assert.Equal(t, "latest report not found\n", w.Body.String())
		assert.Empty(t, w.Header().Get("Location"))
	})

	t.Run("an invalid project id answers 400", func(t *testing.T) {
		s := newExportServer(t, &stubGenerator{})

		w := callWithPath(s.latestReport, http.MethodGet, "/projects/Bad%20Id/latest-report",
			nil, map[string]string{"id": "Bad Id"})

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Empty(t, w.Header().Get("Location"))
	})
}
