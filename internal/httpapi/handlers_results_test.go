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
	"sync"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
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

	// A real Generator rooted at the same dir, not a stub: deleteProject goes
	// through it to take the project's lock, and these tests assert on what
	// actually happened to the directory afterwards. The CLI name is never
	// resolved because nothing here builds a report - only Generate and
	// Version shell out.
	return NewServer(dir, report.New(dir, "unused-cli", 0), RuntimeConfig{}, Versions{}), dir
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
		if got := w.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
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
		defer func() { _ = root.Close() }()

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
		defer func() { _ = root.Close() }()

		src := io.MultiReader(strings.NewReader("partial"), errReader{})

		if _, err := savePart(root, "x.json", src); err == nil {
			t.Fatal("savePart returned nil, want error")
		}

		if _, err := os.Stat(filepath.Join(dir, "x.json")); !os.IsNotExist(err) {
			t.Errorf("partially written file was kept (stat err = %v)", err)
		}
		// The scratch file has to go too. A failed upload that leaves its
		// temporary behind would have every retry accumulate one more, and
		// they all count towards the directory looking non-empty.
		if _, err := os.Stat(filepath.Join(dir, "x.json.part")); !os.IsNotExist(err) {
			t.Errorf("temporary file was kept (stat err = %v)", err)
		}
	})

	t.Run("leaves no temporary behind once the file is published", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer func() { _ = root.Close() }()

		if _, err := savePart(root, "x.json", strings.NewReader("hello")); err != nil {
			t.Fatalf("savePart: %v", err)
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "x.json" {
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			t.Errorf("directory holds %v, want only x.json", names)
		}
	})

	t.Run("publishes the file only once all of it is there", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer func() { _ = root.Close() }()

		// Look at the directory from inside the copy, once the first chunk
		// has been written but before the last one has: this is the window a
		// build or the watcher would be reading results in, and the file must
		// not be visible under its final name yet. Copying straight into that
		// name would show them a truncated result and have them take it for a
		// malformed one.
		var seenEarly bool
		src := io.MultiReader(
			strings.NewReader(`{"uuid":`),
			&hookReader{r: strings.NewReader(`"a"}`), fn: func() {
				if _, err := os.Stat(filepath.Join(dir, "x.json")); err == nil {
					seenEarly = true
				}
			}},
		)

		if _, err := savePart(root, "x.json", src); err != nil {
			t.Fatalf("savePart: %v", err)
		}

		if seenEarly {
			t.Error("x.json was visible under its final name while still half-written")
		}

		got, err := os.ReadFile(filepath.Join(dir, "x.json"))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(got) != `{"uuid":"a"}` {
			t.Errorf("file content = %q, want the whole stream", got)
		}
	})

	t.Run("does not write through a symlink out of root", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "escaped.json")
		if err := os.Symlink(outside, filepath.Join(dir, "evil.json")); err != nil {
			t.Fatalf("setup symlink: %v", err)
		}

		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer func() { _ = root.Close() }()

		// Publishing by rename replaces a planted symlink rather than being
		// refused by it: rename acts on the link itself, never on what it
		// points at. That is a change from copying into the final name
		// directly, which os.Root rejected outright - but only in what the
		// call answers, not in where the bytes land. What matters is asserted
		// below: nothing is written outside the root either way, and the link
		// is gone rather than left aimed out of it.
		if _, err := savePart(root, "evil.json", strings.NewReader("x")); err != nil {
			t.Fatalf("savePart: %v", err)
		}

		if _, err := os.Stat(outside); !os.IsNotExist(err) {
			t.Errorf("data escaped to %q (stat err = %v)", outside, err)
		}

		fi, err := os.Lstat(filepath.Join(dir, "evil.json"))
		if err != nil {
			t.Fatalf("Lstat: %v", err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Error("evil.json is still a symlink pointing out of the root")
		}
	})

	t.Run("does not write through a symlink planted under a scratch name", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "escaped.json")
		// The scratch name carries a random suffix, so an attacker cannot
		// plant anything under it in advance - this is the name it used to
		// have. Either way the scratch file goes through root, which refuses
		// any name that resolves outside it.
		if err := os.Symlink(outside, filepath.Join(dir, "evil.json.part")); err != nil {
			t.Fatalf("setup symlink: %v", err)
		}

		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer func() { _ = root.Close() }()

		if _, err := savePart(root, "evil.json", strings.NewReader("x")); err != nil {
			t.Fatalf("savePart: %v", err)
		}
		if _, err := os.Stat(outside); !os.IsNotExist(err) {
			t.Errorf("data escaped to %q (stat err = %v)", outside, err)
		}
		if got := string(readFileT(t, filepath.Join(dir, "evil.json"))); got != "x" {
			t.Errorf("evil.json = %q, want %q", got, "x")
		}
	})

	t.Run("an empty part leaves the file already on disk alone", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte("good"), 0o644); err != nil {
			t.Fatal(err)
		}

		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer func() { _ = root.Close() }()

		n, err := savePart(root, "x.json", strings.NewReader(""))
		if err != nil || n != 0 {
			t.Fatalf("savePart = (%d, %v), want (0, nil)", n, err)
		}

		// A CI job re-posting a result it truncated locally must not destroy
		// the copy the service already holds: publishing an empty file over it
		// replaces a good result with nothing.
		if got := string(readFileT(t, filepath.Join(dir, "x.json"))); got != "good" {
			t.Errorf("x.json = %q, want the previous content %q", got, "good")
		}
		if names := dirEntries(t, dir); len(names) != 1 {
			t.Errorf("results dir holds %v, want only the published file", names)
		}
	})

	t.Run("concurrent parts of one name do not interleave", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer func() { _ = root.Close() }()

		// environment.properties, categories.json and executor.json are fixed
		// names, unlike the UUID-named results: two CI jobs uploading one of
		// them at the same time land on the same final name. A scratch name
		// shared between them would have both copying into one file at
		// independent offsets, publishing the interleaving.
		a := strings.Repeat("a", 64<<10)
		b := strings.Repeat("b", 64<<10)

		var wg sync.WaitGroup
		for _, content := range []string{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := savePart(root, "environment.properties", strings.NewReader(content)); err != nil {
					t.Errorf("savePart: %v", err)
				}
			}()
		}
		wg.Wait()

		got := string(readFileT(t, filepath.Join(dir, "environment.properties")))
		if got != a && got != b {
			t.Errorf("environment.properties is neither upload whole (len %d), want one of them intact", len(got))
		}
		if names := dirEntries(t, dir); len(names) != 1 {
			t.Errorf("results dir holds %v, want only the published file", names)
		}
	})
}

// readFileT reads path or fails the test.
func readFileT(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

// dirEntries lists the names in dir, so a test can assert that a scratch file
// was cleaned up rather than left behind.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

// errReader always fails, standing in for a connection that drops mid-upload.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// hookReader runs fn once, just before the first byte is read out of r. It is
// how these tests reach into the middle of an upload: whatever fn does happens
// with the transfer already under way but not yet finished.
type hookReader struct {
	r    io.Reader
	fn   func()
	once sync.Once
}

func (h *hookReader) Read(p []byte) (int, error) {
	h.once.Do(h.fn)
	return h.r.Read(p)
}

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
	deadlines      []time.Time
	writeDeadlines []time.Time
	err            error // returned instead of recording, when set
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	if d.err != nil {
		return d.err
	}
	d.deadlines = append(d.deadlines, t)
	return nil
}

// SetWriteDeadline records the deadlines exportReport lifts the response past
// the server's WriteTimeout with; read and write deadlines are kept apart so a
// test asserting on one is not satisfied by the other.
func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	if d.err != nil {
		return d.err
	}
	d.writeDeadlines = append(d.writeDeadlines, t)
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

func TestSendResultsLiftsTheWriteDeadline(t *testing.T) {
	s, _ := newTestServer(t, "demo")
	body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

	r := httptest.NewRequest(http.MethodPost, "/projects/demo/results", body)
	r.SetPathValue("id", "demo")
	r.Header.Set("Content-Type", ct)

	before := time.Now()
	rec := newDeadlineRecorder()
	s.sendResults(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	// The server's WriteTimeout is counted from the moment the request headers
	// are read, so it covers the upload itself and not just the reply. A
	// gigabyte-capped upload does not fit in it, and the handler has to push
	// the deadline out or the response to a long upload cannot be written at
	// all - every file lands on disk and the client is told nothing.
	if len(rec.writeDeadlines) != 1 {
		t.Fatalf("got %d write deadlines, want exactly 1", len(rec.writeDeadlines))
	}
	if got, want := rec.writeDeadlines[0], before.Add(uploadWriteDeadline); got.Before(want) {
		t.Errorf("write deadline = %v, want at least %v (uploadWriteDeadline out from the start)", got, want)
	}
}

func TestSendResultsProjectDeletedMidUpload(t *testing.T) {
	s, dir := newTestServer(t, "demo")
	body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

	// The handler opens its os.Root on the results directory before it reads
	// the first byte of the body, so removing the project here lands the
	// upload exactly where a concurrent DELETE /projects/demo would: a root
	// held open on a directory that is no longer attached to anything.
	hooked := &hookReader{r: body, fn: func() {
		if err := os.RemoveAll(filepath.Join(dir, "demo")); err != nil {
			t.Errorf("removing the project mid-upload: %v", err)
		}
	}}

	w := do(s, "demo", hooked, ct)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body)
	}
	// Exactly one message, not two. The 404 branch has to return: falling
	// through into the generic error handling below it writes a second
	// http.Error onto a response that has already been sent, which
	// net/http reports as a superfluous WriteHeader call and which leaves
	// the client reading both answers glued together.
	if got := w.Body.String(); got != "project not found\n" {
		t.Errorf("body = %q, want just the 404 message", got)
	}
}
