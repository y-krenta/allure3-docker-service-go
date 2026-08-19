package httpapi

import (
	"context"

	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

// reportGenerator is the slice of report generation the HTTP layer depends on.
// It is declared here, next to its user, rather than exported from the report
// package: the handlers can then be tested against a stub that returns a chosen
// error, which is the only practical way to reach the "already running" and
// failure branches without racing a real build.
type reportGenerator interface {
	// Start begins a build for projectID and returns once it has been
	// accepted, not once it has finished.
	Start(ctx context.Context, projectID string) error

	// Status returns the state of the last build started for projectID, and
	// false if none ever was.
	Status(projectID string) (report.Status, bool)

	// ClearResults removes projectID's top-level result files, under the
	// same per-project lock Start/Generate use.
	ClearResults(projectID string) error

	// ClearHistory resets projectID's trend history and archived builds,
	// then starts a fresh build under the same per-project lock.
	ClearHistory(ctx context.Context, projectID string) error
}

// Server holds the shared dependencies for HTTP handlers: the base directory
// under which per-project results/reports are stored, and the generator that
// the report endpoints drive.
type Server struct {
	projectsDir string
	reports     reportGenerator
}

// NewServer builds a Server that resolves project storage under projectsDir
// and starts report builds through reports.
func NewServer(projectsDir string, reports reportGenerator) *Server {
	return &Server{projectsDir: projectsDir, reports: reports}
}
