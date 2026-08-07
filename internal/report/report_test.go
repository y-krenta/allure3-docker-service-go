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
// given CLI, with the given projects already created.
func newTestGenerator(t *testing.T, allureBin string, projectIDs ...string) *Generator {
	t.Helper()

	dir := t.TempDir()
	for _, id := range projectIDs {
		if err := projects.CreateDir(dir, id); err != nil {
			t.Fatalf("CreateDir(%q) = %v", id, err)
		}
	}
	return New(dir, allureBin)
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
