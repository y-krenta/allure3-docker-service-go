package report

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// writeLatestTree lays out a published report with nested directories, so the
// assertions cover more than a single file sitting in the root: the prefix,
// the path separator and the recursion all only show up below the top level.
func writeLatestTree(t *testing.T, g *Generator, projectID string, files map[string]string) {
	t.Helper()

	latest := projects.LatestReportDir(g.projectsDir, projectID)
	for name, body := range files {
		path := filepath.Join(latest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

// exportToReader runs ExportLatest into a buffer and hands back a reader over
// the finished archive. Parsing the bytes is itself an assertion: a zip whose
// central directory was never written - the writer left unclosed - fails here.
func exportToReader(t *testing.T, g *Generator, projectID string) *zip.Reader {
	t.Helper()

	var buf bytes.Buffer
	if err := g.ExportLatest(projectID, &buf); err != nil {
		t.Fatalf("ExportLatest(%q) = %v, want nil", projectID, err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("the exported bytes are not a readable zip archive: %v", err)
	}
	return zr
}

// archiveNames returns every entry name in the archive, sorted, so a test can
// compare against an expected set without depending on walk order.
func archiveNames(zr *zip.Reader) []string {
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	return names
}

// readArchiveEntry returns the contents of one named entry.
func readArchiveEntry(t *testing.T, zr *zip.Reader, name string) string {
	t.Helper()

	f, err := zr.Open(name)
	if err != nil {
		t.Fatalf("opening %q inside the archive: %v", name, err)
	}
	// Nothing to salvage from closing a reader: the read either produced the
	// bytes or already failed the test above.
	defer func() { _ = f.Close() }()

	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading %q inside the archive: %v", name, err)
	}
	return string(b)
}

func TestExportLatestArchivesTheWholeTreeUnderOnePrefix(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{
		"index.html":             "<html>",
		"app.js":                 "console.log(1)",
		"widgets/summary.json":   `{"a":1}`,
		"data/attachments/x.txt": "attached",
	})

	got := archiveNames(exportToReader(t, g, "demo"))
	want := []string{
		"demo-report/app.js",
		"demo-report/data/attachments/x.txt",
		"demo-report/index.html",
		"demo-report/widgets/summary.json",
	}
	if !slices.Equal(got, want) {
		t.Errorf("archive entries =\n%v\nwant\n%v", got, want)
	}
}

func TestExportLatestPreservesFileContents(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{
		"index.html":           "<html>the report</html>",
		"widgets/summary.json": `{"passed":3}`,
	})

	zr := exportToReader(t, g, "demo")
	if got := readArchiveEntry(t, zr, "demo-report/index.html"); got != "<html>the report</html>" {
		t.Errorf("index.html in archive = %q", got)
	}
	if got := readArchiveEntry(t, zr, "demo-report/widgets/summary.json"); got != `{"passed":3}` {
		t.Errorf("summary.json in archive = %q", got)
	}
}

// The walk visits the report root and every subdirectory before it reaches a
// file. Each of those would become a junk entry - "demo-report/." for the root -
// if directories were not skipped, and the root would additionally be handed to
// io.Copy, which refuses a directory and aborts the export on its first call.
func TestExportLatestSkipsDirectories(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{
		"index.html":           "<html>",
		"widgets/summary.json": `{"a":1}`,
	})

	for _, name := range archiveNames(exportToReader(t, g, "demo")) {
		switch name {
		case "demo-report/.", "demo-report/widgets", "demo-report/widgets/":
			t.Errorf("archive contains a directory entry %q", name)
		}
	}
}

// A project whose id differs must not borrow another project's prefix: the
// folder the client unpacks is named after what it asked for.
func TestExportLatestNamesThePrefixAfterTheProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "other")
	writeLatestTree(t, g, "other", map[string]string{"index.html": "<html>"})

	got := archiveNames(exportToReader(t, g, "other"))
	want := []string{"other-report/index.html"}
	if !slices.Equal(got, want) {
		t.Errorf("archive entries = %v, want %v", got, want)
	}
}

// The handler answers 404 from its own check before calling in, so this is the
// narrow race where the report disappears afterwards. It has to surface as an
// error rather than a silently empty archive.
func TestExportLatestWithoutAReportIsAnError(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	var buf bytes.Buffer
	if err := g.ExportLatest("demo", &buf); err == nil {
		t.Fatal("ExportLatest on a project with no published report returned nil")
	}
}

func TestExportLatestWaitsForARunningBuild(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{"index.html": "<html>"})

	// Stand in for a build in flight by holding the project's lock directly.
	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		done <- g.ExportLatest("demo", &buf)
	}()

	select {
	case err := <-done:
		t.Fatalf("ExportLatest returned while the project lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExportLatest after unlock = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExportLatest did not proceed after the project lock was released")
	}
}

func TestExportLatestDoesNotBlockOtherProjects(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "busy", "idle")
	writeLatestTree(t, g, "idle", map[string]string{"index.html": "<html>"})

	held := g.lockFor("busy")
	held.Lock()
	defer held.Unlock()

	done := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		done <- g.ExportLatest("idle", &buf)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExportLatest(idle) = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExportLatest(idle) blocked while an unrelated project was building")
	}
}

// An export running across a build would stitch its archive together from two
// different reports. Holding the lock for the whole walk is what prevents that,
// and a build finishing mid-export is exactly the case the lock exists for.
func TestExportLatestAndGenerateDoNotOverlap(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")
	writeLatestTree(t, g, "demo", map[string]string{"index.html": "old"})

	build := make(chan error, 1)
	go func() { build <- g.Generate(context.Background(), "demo") }()

	// Give the build time to claim the lock, then export against it.
	time.Sleep(100 * time.Millisecond)

	var buf bytes.Buffer
	if err := g.ExportLatest("demo", &buf); err != nil {
		t.Fatalf("ExportLatest = %v, want nil", err)
	}
	if err := <-build; err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("the exported bytes are not a readable zip archive: %v", err)
	}

	// The export waited, so it saw the report the build published - never a
	// half-swapped mixture of the two.
	if got := readArchiveEntry(t, zr, "demo-report/index.html"); got != "fresh" {
		t.Errorf("archived index.html = %q, want the report the build published", got)
	}
}

// The writer is handed nothing but io.Writer, so a failing one has to surface
// as an error rather than a truncated archive reported as success.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExportLatestReportsAFailingWriter(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{"index.html": "<html>"})

	if err := g.ExportLatest("demo", failingWriter{err: io.ErrClosedPipe}); err == nil {
		t.Fatal("ExportLatest with a failing writer returned nil")
	}
}
