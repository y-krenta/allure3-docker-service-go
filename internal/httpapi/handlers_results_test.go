package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	t.Run("re-uploading a name overwrites instead of duplicating", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")

		first, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"first"}`})
		if w := do(s, "demo", first, ct); w.Code != http.StatusOK {
			t.Fatalf("first upload: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}

		second, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"second"}`})
		if w := do(s, "demo", second, ct); w.Code != http.StatusOK {
			t.Fatalf("second upload: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
		}

		resultsDir := projects.ResultsDir(dir, "demo")

		entries, err := os.ReadDir(resultsDir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("results dir holds %d entries, want 1", len(entries))
		}

		got, err := os.ReadFile(filepath.Join(resultsDir, "a-result.json"))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(got) != `{"uuid":"second"}` {
			t.Errorf("content = %s, want the second upload to have replaced the first", got)
		}
	})

	t.Run("rejects multipart without a boundary", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")
		body, _ := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

		// Media type passes the Content-Type check, but MultipartReader cannot
		// split the body without the boundary parameter.
		w := do(s, "demo", body, "multipart/form-data")

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
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

func TestHandleMaxBytesError(t *testing.T) {
	t.Run("answers 413 and names the limit", func(t *testing.T) {
		w := httptest.NewRecorder()

		// Wrapped on purpose: the real error arrives from io.Copy inside
		// savePart, already wrapped with %w, so errors.As must unwrap it.
		err := fmt.Errorf("copy data: %w", &http.MaxBytesError{Limit: 1024})

		if !handleMaxBytesError(w, err) {
			t.Fatal("handleMaxBytesError returned false, want true")
		}
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
		}
		if !strings.Contains(w.Body.String(), "1024") {
			t.Errorf("body = %q, want it to name the 1024 byte limit", w.Body)
		}
	})

	t.Run("ignores other errors and writes nothing", func(t *testing.T) {
		w := httptest.NewRecorder()

		if handleMaxBytesError(w, io.ErrUnexpectedEOF) {
			t.Fatal("handleMaxBytesError returned true, want false")
		}
		if w.Body.Len() != 0 {
			t.Errorf("body = %q, want no response written", w.Body)
		}
	})
}

// deadlineRecorder is a ResponseWriter that supports read deadlines and
// records every deadline set on it. httptest.ResponseRecorder alone does not
// support deadlines, so without this stub the handler can only ever be tested
// on the "not supported" path.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	err       error // returned instead of recording, when set
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	if d.err != nil {
		return d.err
	}
	d.deadlines = append(d.deadlines, t)
	return nil
}

func newDeadlineRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func TestIdleTimeoutBody(t *testing.T) {
	t.Run("refreshes the deadline on every read", func(t *testing.T) {
		rec := newDeadlineRecorder()
		const idle = time.Minute

		body := &idleTimeoutBody{
			ReadCloser: io.NopCloser(strings.NewReader("abcdef")),
			rc:         http.NewResponseController(rec),
			idle:       idle,
		}

		before := time.Now()

		// Read in 2-byte chunks: 3 chunks plus the read that reports EOF.
		reads := 0
		buf := make([]byte, 2)
		for {
			_, err := body.Read(buf)
			reads++
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
		}

		if reads != 4 {
			t.Fatalf("got %d reads, want 4", reads)
		}
		if len(rec.deadlines) != reads {
			t.Fatalf("got %d deadlines for %d reads, want one per read", len(rec.deadlines), reads)
		}

		// Every deadline must sit roughly idle ahead of the moment it was set,
		// and they must never move backwards.
		for i, d := range rec.deadlines {
			if d.Before(before.Add(idle)) {
				t.Errorf("deadline %d = %v, want at least %v ahead", i, d, idle)
			}
			if i > 0 && d.Before(rec.deadlines[i-1]) {
				t.Errorf("deadline %d moved backwards: %v after %v", i, d, rec.deadlines[i-1])
			}
		}
	})

	t.Run("fails the read when the deadline cannot be set", func(t *testing.T) {
		rec := newDeadlineRecorder()
		rec.err = http.ErrNotSupported

		src := strings.NewReader("abcdef")
		body := &idleTimeoutBody{
			ReadCloser: io.NopCloser(src),
			rc:         http.NewResponseController(rec),
			idle:       time.Minute,
		}

		n, err := body.Read(make([]byte, 2))
		if !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("Read err = %v, want %v", err, http.ErrNotSupported)
		}
		if n != 0 {
			t.Errorf("Read returned %d bytes, want 0", n)
		}
		if src.Len() != 6 {
			t.Errorf("source was consumed (%d bytes left of 6), want it untouched", src.Len())
		}
	})
}

func TestSendResultsSetsReadDeadlines(t *testing.T) {
	s, dir := newTestServer(t, "demo")
	body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

	r := httptest.NewRequest(http.MethodPost, "/projects/demo/results", body)
	r.SetPathValue("id", "demo")
	r.Header.Set("Content-Type", ct)

	rec := newDeadlineRecorder()
	s.sendResults(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(projects.ResultsDir(dir, "demo"), "a-result.json")); err != nil {
		t.Errorf("stat uploaded file: %v", err)
	}

	// One deadline comes from the handler's support probe; the rest prove the
	// body was actually wrapped and refreshed while the upload was read.
	if len(rec.deadlines) < 2 {
		t.Fatalf("got %d deadlines, want the probe plus at least one per-read refresh", len(rec.deadlines))
	}
}
