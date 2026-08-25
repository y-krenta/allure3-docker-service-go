package httpapi

import (
	"context"
	"io"
	"time"

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

	// Delete removes projectID's directory tree, under the same per-project
	// lock Start/Generate use.
	Delete(projectID string) error

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
// The type carries no json tags: it is what the server holds, not what the
// endpoint sends. getConfig converts it to configResponse, which is where the
// field names and units of the wire format are decided.
type RuntimeConfig struct {
	KeepHistory       bool
	KeepHistoryLatest int
	CheckResultsEvery time.Duration
}

// Server holds the shared dependencies for HTTP handlers: the base directory
// under which per-project results/reports are stored, and the generator that
// the report endpoints drive.
type Server struct {
	projectsDir string
	reports     reportGenerator
	cfg         RuntimeConfig
	versions    Versions
}

// Versions carries the two version strings the meta endpoints report. It
// exists so that they cannot be swapped: as two string parameters they are
// interchangeable to the compiler, and a transposition would surface only as a
// running service telling a human the wrong two numbers.
//
// They arrive from different places, and neither can change while the process
// runs. Allure is asked of the CLI itself at startup - getVersion explains why
// that beats trusting the version the image was built with. Service is stamped
// into the binary at link time with -X main.serviceVersion, and is "dev" in
// every build that does not pass it.
type Versions struct {
	Allure  string
	Service string
}

// NewServer builds a Server that resolves project storage under projectsDir,
// starts report builds through reports, and answers the meta endpoints with
// cfg and versions. Both of the last two are assembled once in main, from
// three sources that cannot change afterwards - the environment, the Allure
// CLI and a string the linker stamped into this binary - and are immutable for
// the life of the process.
func NewServer(projectsDir string, reports reportGenerator, cfg RuntimeConfig, versions Versions) *Server {
	return &Server{projectsDir: projectsDir, reports: reports, cfg: cfg, versions: versions}
}
