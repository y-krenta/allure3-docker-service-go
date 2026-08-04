package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// uploadFile is one part of a multipart body built by multipartBody.
type uploadFile struct {
	field   string
	name    string
	content string
}

// multipartBody renders files as a multipart/form-data body and returns it
// together with the matching Content-Type header (which carries the boundary).
func multipartBody(t *testing.T, files ...uploadFile) (io.Reader, string) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for _, f := range files {
		part, err := w.CreateFormFile(f.field, f.name)
		if err != nil {
			t.Fatalf("CreateFormFile(%q, %q): %v", f.field, f.name, err)
		}
		if _, err := io.WriteString(part, f.content); err != nil {
			t.Fatalf("write part %q: %v", f.name, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	return &buf, w.FormDataContentType()
}

// newTestServer returns a Server backed by a fresh temp dir that already
// contains the given projects, plus that dir.
func newTestServer(t *testing.T, projectIDs ...string) (*Server, string) {
	t.Helper()

	dir := t.TempDir()
	for _, id := range projectIDs {
		if err := projects.CreateDir(dir, id); err != nil {
			t.Fatalf("setup project %q: %v", id, err)
		}
	}

	return NewServer(dir), dir
}

// do sends one request to sendResults and returns the recorded response.
func do(s *Server, id string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/projects/"+id+"/results", body)
	r.SetPathValue("id", id)
	r.Header.Set("Content-Type", contentType)

	w := httptest.NewRecorder()
	s.sendResults(w, r)

	return w
}

func TestSendResults(t *testing.T) {
	t.Run("stores files and reports them", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		body, ct := multipartBody(t,
			uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`},
			uploadFile{"files[]", "b-result.json", `{"uuid":"b"}`},
		)

		w := do(s, "demo", body, ct)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}

		var got sendResultsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response %q: %v", w.Body, err)
		}
		if got.Count != 2 || len(got.Files) != 2 {
			t.Fatalf("response = %+v, want 2 processed files", got)
		}

		for _, name := range []string{"a-result.json", "b-result.json"} {
			path := filepath.Join(projects.ResultsDir(dir, "demo"), name)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("stat %q: %v", path, err)
			}
		}
	})

	t.Run("empty files are skipped and not left on disk", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		body, ct := multipartBody(t,
			uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`},
			uploadFile{"files[]", "empty.json", ""},
		)

		w := do(s, "demo", body, ct)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}

		var got sendResultsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response %q: %v", w.Body, err)
		}
		if got.Count != 1 {
			t.Fatalf("processed_files_count = %d, want 1", got.Count)
		}

		path := filepath.Join(projects.ResultsDir(dir, "demo"), "empty.json")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("empty.json still on disk (stat err = %v), want it removed", err)
		}
	})

	t.Run("path traversal is stripped to a base name", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		body, ct := multipartBody(t,
			uploadFile{"files[]", "../../../../pwned.json", `{"uuid":"a"}`},
		)

		w := do(s, "demo", body, ct)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}

		inside := filepath.Join(projects.ResultsDir(dir, "demo"), "pwned.json")
		if _, err := os.Stat(inside); err != nil {
			t.Errorf("stat %q: %v", inside, err)
		}
		outside := filepath.Join(dir, "pwned.json")
		if _, err := os.Stat(outside); !os.IsNotExist(err) {
			t.Errorf("file escaped to %q (stat err = %v)", outside, err)
		}
	})

	t.Run("rejects unusable file names", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")
		body, ct := multipartBody(t,
			uploadFile{"files[]", "rep ort.json", `{"uuid":"a"}`},
		)

		w := do(s, "demo", body, ct)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
	})

	t.Run("rejects a non-multipart body", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")

		w := do(s, "demo", strings.NewReader(`{}`), "application/json")

		if w.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusUnsupportedMediaType)
		}
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)
		body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

		w := do(s, "BADID", body, ct)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("unknown project is not found", func(t *testing.T) {
		s, _ := newTestServer(t)
		body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

		w := do(s, "nosuch", body, ct)

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
		}
	})

	t.Run("ignores parts sent under another field name", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")
		body, ct := multipartBody(t, uploadFile{"wrong", "a-result.json", `{"uuid":"a"}`})

		w := do(s, "demo", body, ct)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
		}
	})
}

func TestSavePart(t *testing.T) {
	t.Run("writes the whole stream", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer root.Close()

		n, err := savePart(root, "x.json", strings.NewReader("hello"))
		if err != nil {
			t.Fatalf("savePart returned unexpected error: %v", err)
		}
		if n != 5 {
			t.Errorf("savePart wrote %d bytes, want 5", n)
		}

		got, err := os.ReadFile(filepath.Join(root.Name(), "x.json"))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(got) != "hello" {
			t.Errorf("file content = %q, want %q", got, "hello")
		}
	})

	t.Run("removes the file when the source fails", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer root.Close()

		src := io.MultiReader(strings.NewReader("partial"), errReader{})

		if _, err := savePart(root, "x.json", src); err == nil {
			t.Fatal("savePart returned nil, want error")
		}

		if _, err := os.Stat(filepath.Join(dir, "x.json")); !os.IsNotExist(err) {
			t.Errorf("partially written file was kept (stat err = %v)", err)
		}
	})

	t.Run("refuses to follow a symlink out of root", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "escaped.json")
		if err := os.Symlink(outside, filepath.Join(dir, "evil.json")); err != nil {
			t.Fatalf("setup symlink: %v", err)
		}

		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer root.Close()

		if _, err := savePart(root, "evil.json", strings.NewReader("x")); err == nil {
			t.Fatal("savePart followed the symlink, want an error")
		}
		if _, err := os.Stat(outside); !os.IsNotExist(err) {
			t.Errorf("data escaped to %q (stat err = %v)", outside, err)
		}
	})
}

// errReader always fails, standing in for a connection that drops mid-upload.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
