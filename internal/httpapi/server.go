package httpapi

import (
	"context"
	"io"

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

	// ExportLatest streams projectID's published report into w as a zip
	// archive, holding the same per-project lock. It takes an io.Writer
	// rather than the ResponseWriter so the report package stays free of
	// HTTP, and so a test can export into a buffer.
	ExportLatest(projectID string, w io.Writer) error
}

// RuntimeConfig is the subset of the service's settings that getConfig
// publishes, and it doubles as the wire format for that endpoint. It is
// declared here rather than reusing config.Config for two reasons. Only main
// reads the environment, so the HTTP layer never imports config; and the
// endpoint is a chosen subset, not a dump - config.Config also holds the
// projects directory and the Allure binary path, which describe the host's
// filesystem and are nobody's business outside the process. Settings that are
// parsed but not yet acted on (security, TLS, dev mode, storage optimisation)
// stay out too: reporting them would state a behaviour the service does not
// have. Each belongs here once its code exists.
//
// CheckResultsEverySeconds is a plain int, not a time.Duration, on purpose.
// Duration is an int64 of nanoseconds with no MarshalJSON, so encoding/json
// would silently publish 30000000000 where the field promises seconds. The
// conversion happens in main, where the Duration comes from.
type RuntimeConfig struct {
	KeepHistory              bool `json:"keep_history"`
	KeepHistoryLatest        int  `json:"keep_history_latest"`
	CheckResultsEverySeconds int  `json:"check_results_every_seconds"`
}

// Server holds the shared dependencies for HTTP handlers: the base directory
// under which per-project results/reports are stored, and the generator that
// the report endpoints drive.
type Server struct {
	projectsDir string
	reports     reportGenerator
	cfg         RuntimeConfig
}

// NewServer builds a Server that resolves project storage under projectsDir,
// starts report builds through reports, and reports cfg from the config
// endpoint.
func NewServer(projectsDir string, reports reportGenerator, cfg RuntimeConfig) *Server {
	return &Server{projectsDir: projectsDir, reports: reports, cfg: cfg}
}
