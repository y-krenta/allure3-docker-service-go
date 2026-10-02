package watcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

func writeResult(t *testing.T, root, id, name, content string) {
	t.Helper()

	dir := projects.ResultsDir(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func writeReport(t *testing.T, root, id string, at time.Time) {
	t.Helper()

	dir := projects.LatestReportDir(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	index := filepath.Join(dir, "index.html")
	if err := os.WriteFile(index, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write %s: %v", index, err)
	}
	for _, p := range []string{index, dir} {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}
}

func dateResults(t *testing.T, root, id string, at time.Time) {
	t.Helper()

	dir := projects.ResultsDir(root, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(dir, e.Name()), at, at); err != nil {
			t.Fatalf("chtimes %s: %v", e.Name(), err)
		}
	}
}

var upToDate = time.Now().Add(time.Hour)

type recorder struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (r *recorder) start(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.ids = append(r.ids, id)
	return r.err
}

func (r *recorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.ids...)
}

// An empty results directory has to fingerprint like a project never seen: the
// zero value a missing map entry reads as.
func TestScanEmptyDirIsZeroFingerprint(t *testing.T) {
	fp, err := scan(t.TempDir())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if fp != (fingerprint{}) {
		t.Errorf("scan of empty dir = %+v, want zero value", fp)
	}
}

func TestScanCountsFilesAndSkipsDirs(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte("!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	fp, err := scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if fp.count != 2 {
		t.Errorf("count = %d, want 2 (the nested directory must not be counted)", fp.count)
	}
	if fp.size != 6 {
		t.Errorf("size = %d, want 6", fp.size)
	}
}

func TestScanTracksNewestModTime(t *testing.T) {
	dir := t.TempDir()

	old := filepath.Join(dir, "old.json")
	recent := filepath.Join(dir, "recent.json")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recent, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(recent)
	if err != nil {
		t.Fatal(err)
	}
	want := info.ModTime().UnixNano()

	fp, err := scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if fp.newest != want {
		t.Errorf("newest = %d, want %d (the later of the two mtimes)", fp.newest, want)
	}
}

func TestScanIgnoresExecutorFile(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "a-result.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, projects.ExecutorFileName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterExecutor, err := scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if afterExecutor != before {
		t.Errorf("fingerprint changed after writing %s: before=%+v after=%+v", projects.ExecutorFileName, before, afterExecutor)
	}

	if err := os.WriteFile(filepath.Join(dir, "b-result.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterResult, err := scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if afterResult == afterExecutor {
		t.Error("fingerprint did not change after writing an ordinary result file")
	}
}

func TestScanMissingDirReturnsError(t *testing.T) {
	_, err := scan(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("scan of a missing directory returned nil error")
	}
}

// A restart must not rebuild every project: results older than the published
// report are only remembered.
func TestSweepWarmUpSkipsAnUpToDateReport(t *testing.T) {
	root := t.TempDir()
	base := time.Now().Add(-2 * time.Hour)
	writeResult(t, root, "proj", "a-result.json", "{}")
	dateResults(t, root, "proj", base)
	writeReport(t, root, "proj", base.Add(time.Hour))

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)

	if got := rec.calls(); len(got) != 0 {
		t.Errorf("warm-up pass started builds for %v, want none", got)
	}
	if _, ok := seen["proj"]; !ok {
		t.Error("warm-up pass did not record a fingerprint, so the next tick would rebuild")
	}
}

// Results uploaded after the last report - while the service was down, or in
// the first interval after it came up - are built by the warm-up pass, not
// taken for the baseline.
func TestSweepWarmUpBuildsResultsNewerThanTheReport(t *testing.T) {
	root := t.TempDir()
	base := time.Now().Add(-2 * time.Hour)
	writeResult(t, root, "proj", "a-result.json", "{}")
	dateResults(t, root, "proj", base.Add(time.Hour))
	writeReport(t, root, "proj", base)

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)

	if got := rec.calls(); len(got) != 1 || got[0] != "proj" {
		t.Fatalf("warm-up pass started builds for %v, want [proj]", got)
	}

	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 1 {
		t.Errorf("calls after an unchanged tick = %v, want the build not to be repeated", got)
	}
}

// A report dated like its newest result is the build of those results.
func TestSweepWarmUpSkipsAReportDatedLikeItsResults(t *testing.T) {
	root := t.TempDir()
	at := time.Now().Add(-time.Hour)
	writeResult(t, root, "proj", "a-result.json", "{}")
	dateResults(t, root, "proj", at)
	writeReport(t, root, "proj", at)

	rec := &recorder{}
	sweep(context.Background(), root, map[string]fingerprint{}, rec.start, true)

	if got := rec.calls(); len(got) != 0 {
		t.Errorf("warm-up pass started builds for %v, want none", got)
	}
}

// A report the watcher cannot stat counts as unbuilt: a needless build costs
// seconds, a skipped one loses the run.
func TestSweepWarmUpBuildsWhenTheReportCannotBeChecked(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")
	writeReport(t, root, "proj", upToDate)

	reports := projects.ReportsDir(root, "proj")
	if err := os.Chmod(reports, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(reports, 0o755) })

	rec := &recorder{}
	sweep(context.Background(), root, map[string]fingerprint{}, rec.start, true)

	if got := rec.calls(); len(got) != 1 || got[0] != "proj" {
		t.Errorf("warm-up pass started builds for %v, want [proj]", got)
	}
}

func TestSweepWarmUpBuildsResultsThatHaveNoReport(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)

	if got := rec.calls(); len(got) != 1 || got[0] != "proj" {
		t.Errorf("warm-up pass started builds for %v, want [proj]", got)
	}
}

func TestSweepStartsOnChange(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")
	writeReport(t, root, "proj", upToDate)

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)
	writeResult(t, root, "proj", "b-result.json", "{}")
	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 1 || got[0] != "proj" {
		t.Fatalf("calls = %v, want [proj]", got)
	}

	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 1 {
		t.Errorf("calls after an unchanged tick = %v, want the build not to be repeated", got)
	}
}

// A build refused as already running must not consume the change, or the
// results that arrived during the previous build would never be published.
func TestSweepKeepsFingerprintWhenAlreadyRunning(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")
	writeReport(t, root, "proj", upToDate)

	rec := &recorder{err: report.ErrAlreadyRunning}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)
	writeResult(t, root, "proj", "b-result.json", "{}")
	sweep(context.Background(), root, seen, rec.start, false)
	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 2 {
		t.Errorf("calls = %v, want the refused change to be retried on the next tick", got)
	}
}

// A build already running is the normal case when CI starts one and the
// watcher sees the same upload; logging it as an error would page someone
// every tick.
func TestSweepDoesNotLogAlreadyRunningAsAnError(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")
	writeReport(t, root, "proj", upToDate)
	rec := &recorder{err: fmt.Errorf("%w: proj", report.ErrAlreadyRunning)}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)
	writeResult(t, root, "proj", "b-result.json", "{}")
	sweep(context.Background(), root, seen, rec.start, false)

	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a refused duplicate was logged as an error:\n%s", logs.String())
	}
}

func TestSweepKeepsFingerprintOnError(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")
	writeReport(t, root, "proj", upToDate)

	rec := &recorder{err: errors.New("boom")}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)
	writeResult(t, root, "proj", "b-result.json", "{}")
	sweep(context.Background(), root, seen, rec.start, false)
	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 2 {
		t.Errorf("calls = %v, want a failed start to be retried", got)
	}
}

// An emptied results directory has nothing to build until the next upload
// changes it again, so it is not retried every tick.
func TestSweepConsumesAChangeWithNothingLeftToBuild(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")
	writeReport(t, root, "proj", upToDate)

	rec := &recorder{err: report.ErrNoResults}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)
	if err := os.Remove(filepath.Join(projects.ResultsDir(root, "proj"), "a-result.json")); err != nil {
		t.Fatal(err)
	}
	sweep(context.Background(), root, seen, rec.start, false)
	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 1 {
		t.Errorf("calls = %v, want an empty results directory not to be retried", got)
	}
}

func TestSweepIgnoresDirectoriesThatAreNotProjects(t *testing.T) {
	root := t.TempDir()

	writeResult(t, root, "NotAProject", "a-result.json", "{}")

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, false)
	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 0 {
		t.Errorf("calls = %v, want a directory that is not a project to be left alone", got)
	}
}

func TestSweepIgnoresProjectsWithNothingToBuild(t *testing.T) {
	root := t.TempDir()

	if err := os.MkdirAll(projects.ResultsDir(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
	if len(seen) != 0 {
		t.Errorf("seen = %v, want no entries recorded", seen)
	}
}

func TestSweepMissingProjectsDirDoesNotPanic(t *testing.T) {
	rec := &recorder{}

	sweep(context.Background(), filepath.Join(t.TempDir(), "gone"), map[string]fingerprint{}, rec.start, false)

	if got := rec.calls(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
}

func TestRunDisabledReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(context.Background(), t.TempDir(), 0, (&recorder{}).start)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run with a non-positive interval did not return")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, t.TempDir(), 10*time.Millisecond, (&recorder{}).start)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestRunStartsBuildAfterWarmUp(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")
	writeReport(t, root, "proj", upToDate)

	started := make(chan string, 4)
	start := func(_ context.Context, id string) error {
		select {
		case started <- id:
		default:
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, root, 10*time.Millisecond, start)
	}()

	time.Sleep(50 * time.Millisecond)
	writeResult(t, root, "proj", "b-result.json", "{}")

	select {
	case id := <-started:
		if id != "proj" {
			t.Errorf("started %q, want proj", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run never started a build for the changed project")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
