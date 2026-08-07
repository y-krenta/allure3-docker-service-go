package report

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// generateTimeout caps a single Allure run. It is generous because a large
// project's report legitimately takes minutes to build; the point is only to
// stop a wedged CLI from holding the project's lock forever.
const generateTimeout = time.Minute * 10

// Generator builds Allure reports from the results stored under a project's
// directory. One instance is meant to be shared by the whole process: it is
// safe for concurrent use, and it serializes generation per project, so two
// callers (an API request and the background watcher) never build the same
// project at once while different projects still build in parallel.
//
// Use New to obtain an instance; the zero value is not usable.
type Generator struct {
	projectsDir string // base directory holding every project's results and reports
	allureBin   string // name or path of the Allure CLI executable

	// mu guards locks. Each entry is the lock of a single project, keyed by
	// project ID, and is held for the whole duration of that project's build.
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// ErrProjectNotFound is returned by Generate when the project has no results
// directory under the generator's projects dir. Callers can test for it with
// errors.Is even when it is wrapped with extra context.
var ErrProjectNotFound = errors.New("project not found")

// New builds a Generator that reads and writes project data under projectsDir
// and shells out to allureBin, the name or path of the Allure CLI executable.
// It returns a pointer because a Generator carries a mutex and must never be
// copied.
func New(projectsDir, allureBin string) *Generator {
	return &Generator{
		projectsDir: projectsDir,
		allureBin:   allureBin,
		locks:       make(map[string]*sync.Mutex),
	}
}

// lockFor returns the mutex dedicated to projectID, creating it on first use.
// Repeated calls for the same project always return the same mutex, which is
// what makes the per-project serialization in Generate work; different
// projects get independent mutexes and never block each other.
//
// The returned mutex is not locked: locking and unlocking is the caller's job.
// g.mu is held only for the map lookup, never for the build itself.
func (g *Generator) lockFor(projectID string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()

	m, ok := g.locks[projectID]
	if !ok {
		m = new(sync.Mutex)
		g.locks[projectID] = m
	}
	return m
}

// Generate builds the report for projectID from the results currently on
// disk. Builds of the same project are serialized: a caller that arrives
// while another build is running waits for it to finish. Builds of different
// projects run in parallel.
//
// The report is staged in the project's TmpRoot and only published once the
// CLI has succeeded, by renaming it over the previous build. A build that
// fails, times out, or is killed therefore leaves the report users are
// currently browsing untouched; the previous build is restored if publishing
// itself fails. Stale staging directories left by an earlier crash are cleared
// at the start of each build, under the project's lock.
//
// It returns ErrProjectNotFound (wrapped) if the project has no results
// directory, an error wrapping ctx.Err() if the context is already done by
// the time the project lock is acquired, and a descriptive error for any
// other failure. Callers should match with errors.Is rather than on the
// message.
func (g *Generator) Generate(ctx context.Context, projectID string) error {
	err := projects.ValidateProjectID(projectID)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}

	_, err = os.Stat(projects.ResultsDir(g.projectsDir, projectID))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if err != nil {
		return fmt.Errorf("checking for results directory: %w", err)
	}

	mu := g.lockFor(projectID)
	mu.Lock()
	defer mu.Unlock()

	if ctx.Err() != nil {
		return fmt.Errorf("waiting for project lock: %w", ctx.Err())
	}

	tmp := projects.TmpRoot(g.projectsDir, projectID)

	err = os.RemoveAll(tmp)
	if err != nil {
		return fmt.Errorf("cleaning temp dir: %w", err)
	}
	err = os.MkdirAll(tmp, 0755)
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, generateTimeout)
	defer cancel()
	outDir, err := os.MkdirTemp(tmp, "build-*")
	if err != nil {
		return fmt.Errorf("creating output directory in tmpdir: %w", err)
	}
	defer os.RemoveAll(outDir)

	err = g.runAllure(ctx, projects.ResultsDir(g.projectsDir, projectID), outDir)
	if err != nil {
		return fmt.Errorf("running allure: %w", err)
	}

	latest := projects.LatestReportDir(g.projectsDir, projectID)
	old := filepath.Join(tmp, "old")

	err = os.Rename(latest, old)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("renaming old output directory: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			if rErr := os.Rename(old, latest); rErr != nil {
				slog.Error("restoring previous report", "err", rErr,
					"project_id", projectID)
			}
		}
	}()

	err = os.Rename(outDir, latest)
	if err != nil {
		return fmt.Errorf("moving new output directory to final location: %w", err)
	}

	ok = true
	err = os.RemoveAll(old)
	if err != nil {
		slog.Error("removing old report directory",
			"err", err,
			"project_id", projectID)
	}

	return nil
}

// runAllure shells out to the Allure CLI to build a report from resultsDir
// into outDir, which is expected to be empty. No shell is involved, so nothing
// in either path is interpreted; ctx bounds the run and kills the process if
// it expires.
//
// The CLI's stderr is captured and carried into the returned error: without it
// a failed build reports nothing but an exit status. A missing executable is
// reported as an error wrapping exec.ErrNotFound, which is a deployment
// problem rather than a problem with the results.
func (g *Generator) runAllure(ctx context.Context, resultsDir, outDir string) error {
	cmd := exec.CommandContext(
		ctx,
		g.allureBin,
		"generate",
		resultsDir,
		"-o", outDir,
	)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("allure CLI not found: %w", err)
	}

	if err != nil {
		return fmt.Errorf("running allure generate command: %w, stderr: %s", err,
			strings.TrimSpace(stderr.String()))
	}
	return nil
}
