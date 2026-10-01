package e2e

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestExportReportAsZip(t *testing.T) {
	c := newClient(t)
	id := projectID(t)

	c.createProject(id)
	c.run(id, passed(1), failed(2))

	resp, body := c.get("/projects/" + id + "/report/export")
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("export Content-Type = %q, want application/zip", ct)
	}
	if cd, want := resp.Header.Get("Content-Disposition"), `filename="`+id+`-report.zip"`; !strings.Contains(cd, want) {
		t.Errorf("export Content-Disposition = %q, want it to carry %s", cd, want)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("export is not a zip archive: %v", err)
	}

	dir := id + "-report/"
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		rest, ok := strings.CutPrefix(f.Name, dir)
		if !ok {
			t.Fatalf("archive entry %q lies outside %s", f.Name, dir)
		}
		files[rest] = f
	}

	if _, ok := files["index.html"]; !ok {
		t.Errorf("archive has no %sindex.html", dir)
	}

	f, ok := files["summary.json"]
	if !ok {
		t.Fatalf("archive has no %ssummary.json", dir)
	}
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("opening summary.json in the archive: %v", err)
	}
	defer func() { _ = rc.Close() }()
	archived, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading summary.json from the archive: %v", err)
	}

	_, served := c.get("/projects/" + id + "/reports/latest/summary.json")
	if !bytes.Equal(archived, served) {
		t.Errorf("archived summary.json differs from the published one\narchived: %.300s\nserved:   %.300s", archived, served)
	}
}
