package e2e

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExportReportAsZip(t *testing.T) {
	c := newClient(t)
	id := projectID(t)

	c.createProject(id)
	c.run(id, passed(1), failed(2))

	resp, body := c.get("/projects/" + id + "/report/export")
	assert.Equal(t, "application/zip", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), `filename="`+id+`-report.zip"`)

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)

	dir := id + "-report/"
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		rest, ok := strings.CutPrefix(f.Name, dir)
		require.True(t, ok, "archive entry %q lies outside %s", f.Name, dir)
		files[rest] = f
	}

	assert.Contains(t, files, "index.html")

	require.Contains(t, files, "summary.json")
	rc, err := files["summary.json"].Open()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	archived, err := io.ReadAll(rc)
	require.NoError(t, err)

	_, served := c.get("/projects/" + id + "/reports/latest/summary.json")
	assert.Equal(t, string(served), string(archived))
}
