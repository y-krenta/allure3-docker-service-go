package watcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

// writeResult puts a file into a project's results directory, creating the
// directory tree on first use. It is the fixture for "CI uploaded something".
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

// recorder is a StartFunc that remembers which projects it was asked to build
// and answers with a canned error. It is mutex-guarded because Run calls it
// from its own goroutine.
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

func TestScanEmptyDirIsZeroFingerprint(t *testing.T) {
	fp, err := scan(t.TempDir())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	// The zero fingerprint is what a missing map entry yields, so an empty
	// results directory must be indistinguishable from a never-seen project.
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

	// Explicit times rather than sleeping: the file written second is not
	// guaranteed to carry the later mtime on a coarse-grained filesystem.
	base := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recent, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Read the stored mtime back instead of asserting on what we wrote: the
	// filesystem may round it, and the test should not care.
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

func TestScanMissingDirReturnsError(t *testing.T) {
	_, err := scan(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("scan of a missing directory returned nil error")
	}
}

func TestSweepFirstPassOnlyRemembers(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")

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

func TestSweepStartsOnChange(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")

	rec := &recorder{}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true) // warm-up
	writeResult(t, root, "proj", "b-result.json", "{}")      // CI uploads more
	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 1 || got[0] != "proj" {
		t.Fatalf("calls = %v, want [proj]", got)
	}

	// A third sweep with nothing new must stay quiet: the accepted build has
	// to have updated the stored fingerprint.
	sweep(context.Background(), root, seen, rec.start, false)

	if got := rec.calls(); len(got) != 1 {
		t.Errorf("calls after an unchanged tick = %v, want the build not to be repeated", got)
	}
}

func TestSweepKeepsFingerprintWhenAlreadyRunning(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")

	rec := &recorder{err: report.ErrAlreadyRunning}
	seen := map[string]fingerprint{}

	sweep(context.Background(), root, seen, rec.start, true)
	writeResult(t, root, "proj", "b-result.json", "{}")
	sweep(context.Background(), root, seen, rec.start, false) // refused
	sweep(context.Background(), root, seen, rec.start, false) // must retry

	// This is the whole point of the retry rule: a refused build must not
	// consume the change, or the results that arrived during the previous
	// build would never be published.
	if got := rec.calls(); len(got) != 2 {
		t.Errorf("calls = %v, want the refused change to be retried on the next tick", got)
	}
}

func TestSweepKeepsFingerprintOnError(t *testing.T) {
	root := t.TempDir()
	writeResult(t, root, "proj", "a-result.json", "{}")

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

func TestSweepIgnoresProjectsWithNothingToBuild(t *testing.T) {
	root := t.TempDir()

	// An empty results directory: its fingerprint is the zero value, which is
	// exactly what an absent map entry reads as, so it must never look changed.
	if err := os.MkdirAll(projects.ResultsDir(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A project directory with no results directory at all.
	if err := os.MkdirAll(filepath.Join(root, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stray file in the projects root, which is not a project.
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

	// The first tick only records; the change has to be made after it, so
	// wait for the warm-up to have happened before touching the directory.
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

	// Bounded rather than a bare receive: a Run that ignores cancellation
	// should fail this test, not hang the whole package until the go test
	// timeout fires.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
