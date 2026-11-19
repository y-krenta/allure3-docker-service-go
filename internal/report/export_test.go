package report

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

func writeLatestTree(t *testing.T, g *Generator, projectID string, files map[string]string) {
	t.Helper()

	latest := projects.LatestReportDir(g.projectsDir, projectID)
	for name, body := range files {
		path := filepath.Join(latest, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
}

func exportToReader(t *testing.T, g *Generator, projectID string) *zip.Reader {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, g.ExportLatest(projectID, &buf))

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err, "the exported bytes are not a readable zip archive")
	return zr
}

func archiveNames(zr *zip.Reader) []string {
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func readArchiveEntry(t *testing.T, zr *zip.Reader, name string) string {
	t.Helper()

	f, err := zr.Open(name)
	require.NoError(t, err)

	defer func() { _ = f.Close() }()

	b, err := io.ReadAll(f)
	require.NoError(t, err)
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

	assert.ElementsMatch(t, []string{
		"demo-report/app.js",
		"demo-report/data/attachments/x.txt",
		"demo-report/index.html",
		"demo-report/widgets/summary.json",
	}, archiveNames(exportToReader(t, g, "demo")))
}

func TestExportLatestPreservesFileContents(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{
		"index.html":           "<html>the report</html>",
		"widgets/summary.json": `{"passed":3}`,
	})

	zr := exportToReader(t, g, "demo")
	assert.Equal(t, "<html>the report</html>", readArchiveEntry(t, zr, "demo-report/index.html"))
	assert.Equal(t, `{"passed":3}`, readArchiveEntry(t, zr, "demo-report/widgets/summary.json"))
}

func TestExportLatestSkipsDirectories(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{
		"index.html":           "<html>",
		"widgets/summary.json": `{"a":1}`,
	})

	names := archiveNames(exportToReader(t, g, "demo"))
	for _, dir := range []string{"demo-report/.", "demo-report/widgets", "demo-report/widgets/"} {
		assert.NotContains(t, names, dir)
	}
}

func TestExportLatestNamesThePrefixAfterTheProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "other")
	writeLatestTree(t, g, "other", map[string]string{"index.html": "<html>"})

	assert.Equal(t, []string{"other-report/index.html"}, archiveNames(exportToReader(t, g, "other")))
}

func TestExportLatestWithoutAReportIsAnError(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	var buf bytes.Buffer
	assert.Error(t, g.ExportLatest("demo", &buf))
}

func TestExportLatestWaitsForARunningBuild(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{"index.html": "<html>"})

	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		done <- g.ExportLatest("demo", &buf)
	}()

	select {
	case err := <-done:
		require.FailNow(t, "ExportLatest returned while the project lock was held", "err = %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "ExportLatest did not proceed after the project lock was released")
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
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "ExportLatest(idle) blocked while an unrelated project was building")
	}
}

// An export running across a build's swap would stitch its archive together
// from two reports. Holding the project's lock for the whole walk makes it see
// exactly one.
func TestExportLatestAndGenerateDoNotOverlap(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")
	writeLatestTree(t, g, "demo", map[string]string{"index.html": "old"})

	build := make(chan error, 1)
	go func() { build <- g.Generate(context.Background(), "demo") }()

	time.Sleep(100 * time.Millisecond)

	var buf bytes.Buffer
	require.NoError(t, g.ExportLatest("demo", &buf))
	require.NoError(t, <-build)

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err, "the exported bytes are not a readable zip archive")

	assert.Equal(t, "fresh", readArchiveEntry(t, zr, "demo-report/index.html"), "want the report the build published")
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExportLatestReportsAFailingWriter(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")
	writeLatestTree(t, g, "demo", map[string]string{"index.html": "<html>"})

	assert.Error(t, g.ExportLatest("demo", failingWriter{err: io.ErrClosedPipe}))
}
