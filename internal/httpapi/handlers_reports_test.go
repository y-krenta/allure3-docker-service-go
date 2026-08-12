package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

// stubGenerator is a reportGenerator whose answers are set by the test. The
// real one would have to race an actual build to produce "already running" or
// a failure, so the branches that matter here are unreachable through it.
type stubGenerator struct {
	startErr error // returned by Start

	status    report.Status // returned by Status
	hasStatus bool

	startedWith []string // project IDs Start was called with, in order
}

func (g *stubGenerator) Start(_ context.Context, projectID string) error {
	g.startedWith = append(g.startedWith, projectID)
	return g.startErr
}

func (g *stubGenerator) Status(string) (report.Status, bool) {
	return g.status, g.hasStatus
}

// newStubServer returns a Server whose only working dependency is gen; the
// report endpoints never touch projectsDir.
func newStubServer(gen *stubGenerator) *Server {
	return NewServer("unused-dir", gen)
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

	// decode returns the response body as a map, so a test can assert which
	// keys are present, not merely what they decode into.
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
			// omitempty would have published the zero time here.
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
		// The recorder keeps the first status code, so a handler that forgot
		// to return after the 404 still looks like a 404 here. What gives it
		// away is the status body encoded on top of the message.
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
