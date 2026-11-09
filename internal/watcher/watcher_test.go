package watcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

func writeResult(t *testing.T, root, id, name, content string) {
	t.Helper()

	dir := projects.ResultsDir(root, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func writeReport(t *testing.T, root, id string, at time.Time) {
	t.Helper()

	dir := projects.LatestReportDir(root, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	index := filepath.Join(dir, "index.html")
	require.NoError(t, os.WriteFile(index, []byte("<html></html>"), 0o644))
	for _, p := range []string{index, dir} {
		require.NoError(t, os.Chtimes(p, at, at))
	}
}

func dateResults(t *testing.T, root, id string, at time.Time) {
	t.Helper()

	dir := projects.ResultsDir(root, id)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, os.Chtimes(filepath.Join(dir, e.Name()), at, at))
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
	require.NoError(t, err)

	assert.Zero(t, fp)
}

func TestScanCountsFilesAndSkipsDirs(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte("hello"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte("!"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0o755))

	fp, err := scan(dir)
	require.NoError(t, err)

	assert.Equal(t, 2, fp.count, "the nested directory must not be counted")
	assert.EqualValues(t, 6, fp.size)
}

func TestScanTracksNewestModTime(t *testing.T) {
	dir := t.TempDir()

	old := filepath.Join(dir, "old.json")
	recent := filepath.Join(dir, "recent.json")
	for _, p := range []string{old, recent} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}

	base := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(old, base, base))
	require.NoError(t, os.Chtimes(recent, base.Add(time.Minute), base.Add(time.Minute)))

	info, err := os.Stat(recent)
	require.NoError(t, err)

	fp, err := scan(dir)
	require.NoError(t, err)

	assert.Equal(t, info.ModTime().UnixNano(), fp.newest, "want the later of the two mtimes")
}

func TestScanIgnoresExecutorFile(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-result.json"), []byte("{}"), 0o644))
	before, err := scan(dir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, projects.ExecutorFileName), []byte("{}"), 0o644))
	afterExecutor, err := scan(dir)
	require.NoError(t, err)
	assert.Equal(t, before, afterExecutor, "fingerprint changed after writing %s", projects.ExecutorFileName)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "b-result.json"), []byte("{}"), 0o644))
	afterResult, err := scan(dir)
	require.NoError(t, err)
	assert.NotEqual(t, afterExecutor, afterResult, "fingerprint did not change after writing an ordinary result file")
}

func TestScanMissingDirReturnsError(t *testing.T) {
	_, err := scan(filepath.Join(t.TempDir(), "nope"))

	assert.Error(t, err)
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

	assert.Empty(t, rec.calls())
	assert.Contains(t, seen, "proj", "warm-up pass did not record a fingerprint, so the next tick would rebuild")
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

	require.Equal(t, []string{"proj"}, rec.calls())

	sweep(context.Background(), root, seen, rec.start, false)

	assert.Len(t, rec.calls(), 1, "want the build not to be repeated on an unchanged tick")
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

	assert.Empty(t, rec.calls())
}

// A report the watcher cannot stat counts as unbuilt: a needless build costs
// seconds, a skipped one loses the run.
func TestSweepWarmUpBuildsWhenTheReportCannotBeChecked(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")
	writeReport(t, root, "proj", upToDate)

	reports := projects.ReportsDir(root, "proj")
	require.NoError(t, os.Chmod(reports, 0o000))
	t.Cleanup(func() { _ = os.Chmod(reports, 0o755) })

	rec := &recorder{}
	sweep(context.Background(), root, map[string]fingerprint{}, rec.start, true)

	assert.Equal(t, []string{"proj"}, rec.calls())
}

func TestSweepWarmUpBuildsResultsThatHaveNoReport(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)

	assert.Equal(t, []string{"proj"}, rec.calls())
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

	require.Equal(t, []string{"proj"}, rec.calls())

	sweep(context.Background(), root, seen, rec.start, false)

	assert.Len(t, rec.calls(), 1, "want the build not to be repeated on an unchanged tick")
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

	assert.Len(t, rec.calls(), 2, "want the refused change to be retried on the next tick")
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

	assert.NotContains(t, logs.String(), "level=ERROR")
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

	assert.Len(t, rec.calls(), 2, "want a failed start to be retried")
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
	require.NoError(t, os.Remove(filepath.Join(projects.ResultsDir(root, "proj"), "a-result.json")))
	sweep(context.Background(), root, seen, rec.start, false)
	sweep(context.Background(), root, seen, rec.start, false)

	assert.Len(t, rec.calls(), 1, "want an empty results directory not to be retried")
}

func TestSweepIgnoresDirectoriesThatAreNotProjects(t *testing.T) {
	root := t.TempDir()

	writeResult(t, root, "NotAProject", "a-result.json", "{}")

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, false)
	sweep(context.Background(), root, seen, rec.start, false)

	assert.Empty(t, rec.calls())
}

func TestSweepIgnoresProjectsWithNothingToBuild(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.MkdirAll(projects.ResultsDir(root, "empty"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bare"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o644))

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, false)

	assert.Empty(t, rec.calls())
	assert.Empty(t, seen)
}

func TestSweepMissingProjectsDirDoesNotPanic(t *testing.T) {
	rec := &recorder{}

	sweep(context.Background(), filepath.Join(t.TempDir(), "gone"), map[string]fingerprint{}, rec.start, false)

	assert.Empty(t, rec.calls())
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
		require.FailNow(t, "Run with a non-positive interval did not return")
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
		require.FailNow(t, "Run did not return after its context was cancelled")
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
		assert.Equal(t, "proj", id)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Run never started a build for the changed project")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Run did not return after its context was cancelled")
	}
}
