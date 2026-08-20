package report

import (
	"archive/zip"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// ExportLatest writes projectID's published report into w as a zip archive.
// Every file is placed under a single "<projectID>-report/" directory inside
// the archive, so unpacking it produces one folder rather than scattering a
// few thousand files across the caller's working directory.
//
// It holds the project's build lock for the whole walk. A build publishes its
// report by renaming directories, and an export running across that swap
// would stitch the archive together from two different reports - corrupt in a
// way neither side would notice. The price is that a call made while a build
// is running blocks until that build finishes, which can take minutes.
//
// The archive is streamed into w rather than buffered, so an error can be
// returned after part of it has already been written. A caller serving an
// HTTP request cannot turn that into an error status and can only log it.
//
// A missing report directory is reported as a plain error from the walk, not
// a sentinel: callers establish that the report exists before asking for it.
func (g *Generator) ExportLatest(projectID string, w io.Writer) (err error) {
	base := projectID + "-report"
	mu := g.lockFor(projectID)
	mu.Lock()
	defer mu.Unlock()

	root := projects.LatestReportDir(g.projectsDir, projectID)

	zw := zip.NewWriter(w)
	defer func() {
		zipErr := zw.Close()
		if err == nil {
			err = zipErr
		}
	}()

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry, err := zw.Create(base + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() {
			err := f.Close()
			if err != nil {
				slog.Error("failed to close report file", "err", err, "project_id", projectID, "path", path)
			}
		}()
		_, err = io.Copy(entry, f)
		if err != nil {
			return err
		}
		return nil
	})
}
