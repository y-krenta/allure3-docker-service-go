package report

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

// State is where a project's report generation currently stands. The values
// are part of the HTTP API and travel to clients unchanged, so renaming one is
// a breaking change.
type State string

const (
	// StateRunning means a build is in flight. The report on disk is still
	// the previous one until that build publishes its own.
	StateRunning State = "running"
	// StateSucceeded means the build finished and its report is published.
	StateSucceeded State = "succeeded"
	// StateFailed means the build did not finish. Status.Err says why, and
	// the previously published report is untouched.
	StateFailed State = "failed"
)

// Status is a snapshot of the last generation started for a project. It is
// passed around by value: a caller reads a private copy rather than a view
// into the generator's state, so the running build cannot change the fields
// out from under it.
type Status struct {
	State      State
	StartedAt  time.Time
	FinishedAt time.Time // zero while State is StateRunning
	Err        error     // nil unless State is StateFailed
}

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

	// mu guards both maps below, and is held only for the map operation
	// itself, never for a build: what a build holds for its whole duration is
	// the project's own mutex from locks.
	mu sync.Mutex
	// locks holds one mutex per project, keyed by project ID, serializing
	// builds of that project.
	locks map[string]*sync.Mutex
	// statuses holds the last known state of each project's generation. It is
	// the mailbox between a running build and the callers asking after it.
	statuses map[string]Status
}

var (
	// ErrProjectNotFound is returned by Generate when the project has no results
	// directory under the generator's projects dir. Callers can test for it with
	// errors.Is even when it is wrapped with extra context.
	ErrProjectNotFound = errors.New("project not found")

	// ErrAlreadyRunning is returned by Start when a build for that project is
	// already in flight. It is not quite a failure: the report the caller
	// asked for is being produced, just not by this call.
	ErrAlreadyRunning = errors.New("report generation is already running")

	// ErrNoResults is returned when the project's results directory is
	// empty. Allure builds a report from nothing without complaining, and
	// publishing it would replace the last good report with an empty one,
	// so a build with nothing to build from is refused instead.
	ErrNoResults = errors.New("project has no results to generate a report from")
)

// New builds a Generator that reads and writes project data under projectsDir
// and shells out to allureBin, the name or path of the Allure CLI executable.
// It returns a pointer because a Generator carries a mutex and must never be
// copied.
func New(projectsDir, allureBin string) *Generator {
	return &Generator{
		projectsDir: projectsDir,
		allureBin:   allureBin,
		locks:       make(map[string]*sync.Mutex),
		statuses:    make(map[string]Status),
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

// Status reports the state of the last generation started for projectID, and
// whether there was one at all. A project nobody has generated yet has no
// status rather than a placeholder one, so callers must check the boolean
// before trusting the returned value.
//
// The result is a copy taken under g.mu; it will not change afterwards, even
// while the build it describes is still running.
func (g *Generator) Status(projectID string) (Status, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	st, ok := g.statuses[projectID]
	return st, ok
}

// setStatus records st as the status of projectID, replacing whatever was
// there. The record is overwritten whole rather than merged: only the
// goroutine running a build writes it, and its newer view always supersedes
// the older one.
func (g *Generator) setStatus(projectID string, st Status) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.statuses[projectID] = st
}

// tryStart claims projectID for a build starting at startedAt, and reports
// whether the claim succeeded. A project whose recorded state is StateRunning
// is already claimed; any other state, or no record at all, is free.
//
// The check and the claim happen under a single hold of g.mu deliberately.
// Splitting them into a Status call followed by a setStatus call would leave a
// window in which two callers both see a free project, both claim it and both
// start a build — the second of which would still be running when the first
// reports success.
func (g *Generator) tryStart(projectID string, startedAt time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	st, ok := g.statuses[projectID]
	if ok && st.State == StateRunning {
		return false
	}

	g.statuses[projectID] = Status{State: StateRunning, StartedAt: startedAt}
	return true
}

// checkProject reports whether projectID names a project this generator can
// build: the ID must be well formed, and the project's results directory must
// exist and hold at least one entry. It is the shared precondition of Generate
// and Start, which is why its errors name no particular operation.
//
// A missing results directory is reported as ErrProjectNotFound (wrapped) and
// an empty one as ErrNoResults (wrapped), so callers can tell either from a
// genuine I/O failure with errors.Is. The directory is opened once and probed
// for a single entry rather than listed: whether it holds one result or ten
// thousand, the question is only whether it holds any.
func (g *Generator) checkProject(projectID string) error {
	err := projects.ValidateProjectID(projectID)
	if err != nil {
		return err
	}

	f, err := os.Open(projects.ResultsDir(g.projectsDir, projectID))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if err != nil {
		return fmt.Errorf("checking for results directory: %w", err)
	}
	defer func() { _ = f.Close() }()

	_, err = f.ReadDir(1)
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %s", ErrNoResults, projectID)
	}
	if err != nil {
		return fmt.Errorf("reading results directory: %w", err)
	}

	return nil
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
// directory, ErrNoResults (wrapped) if that directory is empty, an error
// wrapping ctx.Err() if the context is already done by
// the time the project lock is acquired, and a descriptive error for any
// other failure. Callers should match with errors.Is rather than on the
// message.
func (g *Generator) Generate(ctx context.Context, projectID string) error {
	err := g.checkProject(projectID)
	if err != nil {
		return err
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
	defer func() { _ = os.RemoveAll(outDir) }()

	historyPath := projects.HistoryFile(g.projectsDir, projectID)
	copyHistory := filepath.Join(tmp, "history.jsonl")
	err = stageHistory(historyPath, copyHistory)
	if err != nil {
		return fmt.Errorf("copying history: %w", err)
	}
	err = g.runAllure(ctx, projects.ResultsDir(g.projectsDir, projectID), outDir, copyHistory)
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

	// The history is published after the report, and only ever after it. Dying
	// between the two renames costs this run its history entry, which the next
	// build appends again from the same results; dying the other way round
	// would leave the entry already recorded and the next build would append a
	// duplicate. A lost entry heals itself, a duplicated one does not.
	//
	// A failure here is logged rather than returned: the report is live and
	// correct at this point, and reporting the build as failed would be a lie
	// about it.
	err = os.Rename(copyHistory, historyPath)
	if err != nil {
		slog.Error("publishing history", "err", err, "project_id", projectID)
	}
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
// historyPath is the file the CLI reads the project's past runs from and
// appends this one to, which is what gives the report its trends. It is an
// input and an output at once, and the CLI mutates it in place: same inode,
// growing by one line per run, with no staging and no rename of its own. A run
// killed mid-write therefore leaves a half-written line behind, and the CLI
// refuses a history file ending in one - exit 1, empty stderr, no report.
//
// It must be a copy staged for this build, not the project's real history.
// Callers own the copying and the publishing; see Generate.
//
// The CLI's stderr is captured and carried into the returned error: without it
// a failed build reports nothing but an exit status. A missing executable is
// reported as an error wrapping exec.ErrNotFound, which is a deployment
// problem rather than a problem with the results.
func (g *Generator) runAllure(ctx context.Context, resultsDir, outDir, historyPath string) error {
	cmd := exec.CommandContext(
		ctx,
		g.allureBin,
		"awesome",
		resultsDir,
		"--output", outDir,
		"--history-path", historyPath,
	)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("allure CLI not found: %w", err)
	}

	if err != nil {
		return fmt.Errorf("running allure awesome command: %w, stderr: %s", err,
			strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Start begins building the report for projectID in the background and returns
// as soon as the build has been accepted. A nil error means the build started,
// not that it succeeded: the outcome is reported through Status, which shows
// StateRunning from before Start returns until the build replaces it with
// StateSucceeded or StateFailed.
//
// It returns a validation error for a malformed project ID, ErrProjectNotFound
// (wrapped) if the project has no results directory, ErrNoResults (wrapped) if
// that directory is empty, and ErrAlreadyRunning
// (wrapped) if a build for that project is already in flight. Rejecting the
// second caller rather than queueing it keeps a burst of requests from piling
// up builds that would each rebuild what the previous one just built.
//
// ctx is used for its values only. Start strips cancellation from it, so a
// build is not tied to whoever asked for it: the client may disconnect, and the
// report is still published for whoever asks next. What bounds the build is
// Generate's own timeout, which it applies to the context it is given.
//
// The background goroutine is deliberately never waited on, so a shutdown can
// cut a build short. That costs nothing: the report is published by an atomic
// rename, a half-built one is never visible, and its staging directory is
// cleared by the next build.
func (g *Generator) Start(ctx context.Context, projectID string) error {
	err := g.checkProject(projectID)
	if err != nil {
		return err
	}
	startedAt := time.Now()
	resultStart := g.tryStart(projectID, startedAt)
	if !resultStart {
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, projectID)
	}

	ctx = context.WithoutCancel(ctx)
	go func() {
		var err error

		defer func() {
			state := StateSucceeded
			if rec := recover(); rec != nil {
				err = fmt.Errorf("panic generate: %v", rec)
			}

			if err != nil {
				slog.Error("job generate failed", "err", err, "project_id", projectID)
				state = StateFailed
			}

			g.setStatus(projectID, Status{
				StartedAt:  startedAt,
				FinishedAt: time.Now(),
				State:      state,
				Err:        err,
			},
			)

		}()

		err = g.Generate(ctx, projectID)
	}()

	return nil
}

// stageHistory copies the project's history file to dst, where the Allure CLI
// can append to it without the real file being at risk. A missing src is not
// an error: a project builds its first report with no history at all.
func stageHistory(src, dst string) error {
	data, err := os.ReadFile(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
