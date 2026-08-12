package report

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// Shell bodies for the stand-in Allure CLI used by the tests. Both are called
// the way runAllure calls the real thing: generate <resultsDir> -o <outDir>,
// so $4 is the output directory.
const (
	// cliOK mimics a successful build by writing a recognizable report.
	cliOK = "#!/bin/sh\nprintf 'fresh' > \"$4/index.html\"\n"
	// cliFail mimics Allure rejecting the results.
	cliFail = "#!/bin/sh\necho 'boom: broken results' >&2\nexit 3\n"
	// cliSlow succeeds, but takes long enough that a test can observe the
	// build while it is still running.
	cliSlow = "#!/bin/sh\nsleep 0.5\nprintf 'fresh' > \"$4/index.html\"\n"
)

// fakeCLI writes body to an executable file and returns its path, so a test
// can drive Generate without depending on a real Allure installation.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-allure")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing fake CLI: %v", err)
	}
	return path
}

// newTestGenerator returns a Generator rooted at a fresh temp dir, running the
// given CLI, with the given projects already created and holding one result
// file each — a project with an empty results dir is refused before any build
// starts, so tests about building need something to build from.
func newTestGenerator(t *testing.T, allureBin string, projectIDs ...string) *Generator {
	t.Helper()

	dir := t.TempDir()
	for _, id := range projectIDs {
		if err := projects.CreateDir(dir, id); err != nil {
			t.Fatalf("CreateDir(%q) = %v", id, err)
		}
		writeResult(t, dir, id)
	}
	return New(dir, allureBin)
}

// writeResult drops one Allure result file into the project's results dir.
func writeResult(t *testing.T, baseDir, projectID string) {
	t.Helper()

	path := filepath.Join(projects.ResultsDir(baseDir, projectID), "9f0a1c-result.json")
	if err := os.WriteFile(path, []byte(`{"name":"a test"}`), 0o644); err != nil {
		t.Fatalf("writing result file: %v", err)
	}
}

// writeLatest replaces the project's latest report with one containing body,
// standing in for a report left by an earlier build.
func writeLatest(t *testing.T, g *Generator, projectID, body string) {
	t.Helper()

	latest := projects.LatestReportDir(g.projectsDir, projectID)
	if err := os.MkdirAll(latest, 0o755); err != nil {
		t.Fatalf("creating latest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(latest, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing latest report: %v", err)
	}
}

// readLatest returns the body of the project's current latest report.
func readLatest(t *testing.T, g *Generator, projectID string) string {
	t.Helper()

	path := filepath.Join(projects.LatestReportDir(g.projectsDir, projectID), "index.html")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading latest report: %v", err)
	}
	return string(b)
}

func TestLockForReturnsSameMutexPerProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	if a, b := g.lockFor("demo"), g.lockFor("demo"); a != b {
		t.Errorf("lockFor(%q) returned different mutexes on repeated calls", "demo")
	}
	if a, b := g.lockFor("one"), g.lockFor("two"); a == b {
		t.Error("different projects share a mutex, they must not block each other")
	}
}

func TestLockForIsConcurrencySafe(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	// Hammer the registry from many goroutines: -race fails here if mu ever
	// stops guarding the map.
	const goroutines = 50
	done := make(chan *sync.Mutex, goroutines)
	for i := range goroutines {
		go func() {
			g.lockFor(string(rune('a' + i%26))) // churn the map with other keys
			done <- g.lockFor("same")
		}()
	}

	first := <-done
	for range goroutines - 1 {
		if got := <-done; got != first {
			t.Fatal("concurrent lockFor calls produced different mutexes for one project")
		}
	}
}

func TestGenerateRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	if err := g.Generate(t.Context(), "../escape"); err == nil {
		t.Fatal("Generate accepted a project ID containing a path traversal")
	}
}

func TestGenerateUnknownProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	err := g.Generate(t.Context(), "missing")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Generate(missing) = %v, want ErrProjectNotFound", err)
	}
}

// An empty results directory would build an empty report and publish it over
// the last good one, so both entry points refuse it before touching anything.
func TestEmptyResultsDirIsRefused(t *testing.T) {
	newEmptyProject := func(t *testing.T) *Generator {
		t.Helper()

		dir := t.TempDir()
		if err := projects.CreateDir(dir, "demo"); err != nil {
			t.Fatalf("CreateDir: %v", err)
		}
		return New(dir, fakeCLI(t, cliOK))
	}

	t.Run("Generate", func(t *testing.T) {
		g := newEmptyProject(t)

		if err := g.Generate(t.Context(), "demo"); !errors.Is(err, ErrNoResults) {
			t.Fatalf("Generate = %v, want ErrNoResults", err)
		}
	})

	t.Run("Start", func(t *testing.T) {
		g := newEmptyProject(t)

		if err := g.Start(t.Context(), "demo"); !errors.Is(err, ErrNoResults) {
			t.Fatalf("Start = %v, want ErrNoResults", err)
		}
		if st, ok := g.Status("demo"); ok {
			t.Fatalf("Status = %+v, want no status recorded for a rejected build", st)
		}
	})

	t.Run("the previous report survives", func(t *testing.T) {
		g := newEmptyProject(t)
		writeLatest(t, g, "demo", "previous")

		if err := g.Generate(t.Context(), "demo"); !errors.Is(err, ErrNoResults) {
			t.Fatalf("Generate = %v, want ErrNoResults", err)
		}
		if got := readLatest(t, g, "demo"); got != "previous" {
			t.Fatalf("latest report = %q, want the previous one left untouched", got)
		}
	})
}

func TestGenerateSerializesSameProject(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// Simulate a build in flight by holding the project's lock directly.
	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() { done <- g.Generate(context.Background(), "demo") }()

	select {
	case err := <-done:
		t.Fatalf("Generate returned while the project lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Generate after unlock = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not proceed after the project lock was released")
	}
}

func TestGenerateDoesNotBlockOtherProjects(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "busy", "idle")

	held := g.lockFor("busy")
	held.Lock()
	defer held.Unlock()

	done := make(chan error, 1)
	go func() { done <- g.Generate(context.Background(), "idle") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Generate(idle) = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate(idle) blocked while an unrelated project was building")
	}
}

func TestGenerateFirstBuildCreatesLatest(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// A brand new project has no latest report yet: the swap must treat the
	// missing directory as normal rather than as a failure.
	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want %q", got, "fresh")
	}
}

func TestGenerateReplacesPreviousReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")
	writeLatest(t, g, "demo", "stale")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want the newly built %q", got, "fresh")
	}
}

func TestGenerateFailedBuildKeepsPreviousReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliFail), "demo")
	writeLatest(t, g, "demo", "stale")

	err := g.Generate(t.Context(), "demo")
	if err == nil {
		t.Fatal("Generate = nil, want an error when the CLI exits non-zero")
	}
	// The CLI's own diagnostics must survive into the error, otherwise a
	// failed build is undebuggable.
	if !strings.Contains(err.Error(), "boom: broken results") {
		t.Errorf("Generate error = %v, want it to carry the CLI stderr", err)
	}
	// Build-then-swap exists for this: a failed build must not destroy the
	// report users are currently browsing.
	if got := readLatest(t, g, "demo"); got != "stale" {
		t.Errorf("latest report = %q, want the previous %q left untouched", got, "stale")
	}
}

func TestGenerateRemovesTempBuildDirs(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// Junk left behind by a build that was killed before it could clean up.
	tmp := projects.TmpRoot(g.projectsDir, "demo")
	if err := os.MkdirAll(filepath.Join(tmp, "build-stale"), 0o755); err != nil {
		t.Fatalf("seeding stale temp dir: %v", err)
	}

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("reading temp root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temp root still holds %d entries after a build, want it empty", len(entries))
	}
}

func TestRunAllureReportsMissingBinary(t *testing.T) {
	g := newTestGenerator(t, "definitely-not-an-installed-binary", "demo")

	err := g.runAllure(t.Context(), t.TempDir(), t.TempDir())
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("runAllure = %v, want an error wrapping exec.ErrNotFound", err)
	}
}

func TestGenerateWithRealAllure(t *testing.T) {
	if _, err := exec.LookPath("allure"); err != nil {
		t.Skip("allure CLI not installed")
	}

	g := newTestGenerator(t, "allure", "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}
	index := filepath.Join(projects.LatestReportDir(g.projectsDir, "demo"), "index.html")
	if _, err := os.Stat(index); err != nil {
		t.Errorf("real Allure run left no index.html at %s: %v", index, err)
	}
}

func TestGenerateContextDoneWhileWaitingForLock(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	held := g.lockFor("demo")
	held.Lock()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- g.Generate(ctx, "demo") }()

	// Let the goroutine reach the lock, then kill the context and release it:
	// Generate must notice the dead context instead of starting the build.
	time.Sleep(50 * time.Millisecond)
	cancel()
	held.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate = %v, want an error wrapping context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not return after its context was canceled")
	}
}

// waitForState polls until the project's status reaches want, and returns that
// status. Polling rather than a channel keeps the test honest: it observes the
// generator only through its public surface, exactly as an HTTP handler would.
func waitForState(t *testing.T, g *Generator, projectID string, want State) Status {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, ok := g.Status(projectID)
		if ok && st.State == want {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}

	st, ok := g.Status(projectID)
	t.Fatalf("status of %q never reached %q (last: %+v, exists=%v)", projectID, want, st, ok)
	return Status{}
}

func TestStartReturnsBeforeTheBuildFinishes(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	began := time.Now()
	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	// The whole point of Start: the caller is not made to wait for the CLI.
	if waited := time.Since(began); waited > 200*time.Millisecond {
		t.Errorf("Start blocked for %v, want it to return while the build runs", waited)
	}

	// And the build must be visible as running, not silently in flight.
	st, ok := g.Status("demo")
	if !ok || st.State != StateRunning {
		t.Fatalf("status right after Start = %+v (exists=%v), want %q", st, ok, StateRunning)
	}
	if st.StartedAt.IsZero() {
		t.Error("running status has no StartedAt")
	}

	waitForState(t, g, "demo", StateSucceeded)
}

func TestStartRecordsSuccessAndPublishesReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}

	st := waitForState(t, g, "demo", StateSucceeded)
	if st.Err != nil {
		t.Errorf("succeeded status carries an error: %v", st.Err)
	}
	if st.FinishedAt.Before(st.StartedAt) || st.FinishedAt.IsZero() {
		t.Errorf("timestamps make no sense: started %v, finished %v", st.StartedAt, st.FinishedAt)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want the newly built %q", got, "fresh")
	}
}

func TestStartRecordsFailure(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliFail), "demo")
	writeLatest(t, g, "demo", "stale")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil: a build that will fail still starts fine", err)
	}

	st := waitForState(t, g, "demo", StateFailed)
	if st.Err == nil {
		t.Fatal("failed status carries no error, the caller has no way to learn why")
	}
	if !strings.Contains(st.Err.Error(), "boom: broken results") {
		t.Errorf("status error = %v, want it to carry the CLI stderr", st.Err)
	}
	if got := readLatest(t, g, "demo"); got != "stale" {
		t.Errorf("latest report = %q, want the previous %q left untouched", got, "stale")
	}
}

func TestStartRejectsASecondBuildOfTheSameProject(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("first Start = %v, want nil", err)
	}

	err := g.Start(t.Context(), "demo")
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrAlreadyRunning", err)
	}

	// Once the first build is done the project is free again: the claim is a
	// lock on the build, not a permanent mark on the project.
	waitForState(t, g, "demo", StateSucceeded)
	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start after the previous build finished = %v, want nil", err)
	}
	waitForState(t, g, "demo", StateSucceeded)
}

func TestTryStartClaimsExactlyOnceUnderConcurrency(t *testing.T) {
	// The claim exists for exactly this: many callers arriving together, all
	// seeing a free project, and only one being allowed to build it.
	//
	// A check-then-set split into two separately locked operations survives a
	// single round most of the time, because the window between them is a few
	// nanoseconds wide. Repeating the round is what turns "rarely wrong" into
	// "reliably caught". Calling tryStart directly matters too: going through
	// Start puts an os.Stat in front of the claim, which spreads the callers
	// out and hides the race.
	const rounds, callers = 200, 40

	for round := range rounds {
		g := New("unused-dir", "unused-cli") // tryStart never touches disk

		var ready, done sync.WaitGroup
		ready.Add(callers)
		done.Add(callers)

		release := make(chan struct{})
		won := make(chan struct{}, callers)

		for range callers {
			go func() {
				defer done.Done()
				ready.Done()
				<-release // everyone leaves the gate in the same instant
				if g.tryStart("demo", time.Now()) {
					won <- struct{}{}
				}
			}()
		}

		ready.Wait()
		close(release)
		done.Wait()
		close(won)

		if n := len(won); n != 1 {
			t.Fatalf("round %d: %d of %d callers claimed the project, want exactly 1",
				round, n, callers)
		}
	}
}

func TestStatusStaysReadableWhileABuildRuns(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}

	// Holding g.mu for the duration of the build would deadlock the service:
	// every status request would queue behind the CLI.
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		g.Status("demo")
	}()

	select {
	case <-answered:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Status blocked while a build was running: the build is holding g.mu")
	}

	waitForState(t, g, "demo", StateSucceeded)
}

func TestStartIgnoresTheCallersCancellation(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	ctx, cancel := context.WithCancel(context.Background())
	if err := g.Start(ctx, "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	// Stands in for the client disconnecting mid-build. The work is already
	// underway and its result lands on disk, so it must not be thrown away.
	cancel()

	st := waitForState(t, g, "demo", StateSucceeded)
	if st.Err != nil {
		t.Errorf("build reported %v after the caller went away, want it to finish", st.Err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want the build to have published %q", got, "fresh")
	}
}

func TestStartRejectsUnknownAndMalformedProjects(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Start(t.Context(), "missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Start(missing) = %v, want ErrProjectNotFound", err)
	}
	if err := g.Start(t.Context(), "../escape"); err == nil {
		t.Error("Start accepted a project ID containing a path traversal")
	}

	// A rejected call must leave no trace, otherwise a typo would mark a
	// project as running forever.
	if st, ok := g.Status("missing"); ok {
		t.Errorf("rejected Start left a status behind: %+v", st)
	}
}
