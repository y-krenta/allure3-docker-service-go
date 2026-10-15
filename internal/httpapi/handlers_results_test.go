package httpapi

import (
	"bytes"
	"encoding/json"
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

type uploadFile struct {
	field   string
	name    string
	content string
}

func multipartBody(t *testing.T, files ...uploadFile) (io.Reader, string) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for _, f := range files {
		part, err := w.CreateFormFile(f.field, f.name)
		require.NoError(t, err)
		_, err = io.WriteString(part, f.content)
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())

	return &buf, w.FormDataContentType()
}

func newTestServer(t *testing.T, projectIDs ...string) (*Server, string) {
	t.Helper()

	dir := t.TempDir()
	for _, id := range projectIDs {
		require.NoError(t, projects.CreateDir(dir, id))
	}

	return NewServer(dir, report.New(dir, "unused-cli", 0, "https://allure.example.test", 4, 0), RuntimeConfig{}, Versions{}), dir
}

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

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var got sendResultsResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())
		require.Equal(t, 2, got.Count)
		require.Len(t, got.Files, 2)

		for _, name := range []string{"a-result.json", "b-result.json"} {
			assert.FileExists(t, filepath.Join(projects.ResultsDir(dir, "demo"), name))
		}
	})

	t.Run("empty files are skipped and not left on disk", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		body, ct := multipartBody(t,
			uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`},
			uploadFile{"files[]", "empty.json", ""},
		)

		w := do(s, "demo", body, ct)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		var got sendResultsResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())
		require.Equal(t, 1, got.Count)

		_, err := os.Stat(filepath.Join(projects.ResultsDir(dir, "demo"), "empty.json"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("path traversal is stripped to a base name", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")
		body, ct := multipartBody(t,
			uploadFile{"files[]", "../../../../pwned.json", `{"uuid":"a"}`},
		)

		w := do(s, "demo", body, ct)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		assert.FileExists(t, filepath.Join(projects.ResultsDir(dir, "demo"), "pwned.json"))
		_, err := os.Stat(filepath.Join(dir, "pwned.json"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("rejects unusable file names", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")
		body, ct := multipartBody(t,
			uploadFile{"files[]", "rep ort.json", `{"uuid":"a"}`},
		)

		w := do(s, "demo", body, ct)

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	})

	t.Run("rejects a non-multipart body", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")

		w := do(s, "demo", strings.NewReader(`{}`), "application/json")

		assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
	})

	t.Run("rejects an invalid project id", func(t *testing.T) {
		s, _ := newTestServer(t)
		body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

		w := do(s, "BADID", body, ct)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("unknown project is not found", func(t *testing.T) {
		s, _ := newTestServer(t)
		body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

		w := do(s, "nosuch", body, ct)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("re-uploading a name overwrites instead of duplicating", func(t *testing.T) {
		s, dir := newTestServer(t, "demo")

		first, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"first"}`})
		w := do(s, "demo", first, ct)
		require.Equal(t, http.StatusOK, w.Code, "first upload: %s", w.Body)

		second, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"second"}`})
		w = do(s, "demo", second, ct)
		require.Equal(t, http.StatusOK, w.Code, "second upload: %s", w.Body)

		resultsDir := projects.ResultsDir(dir, "demo")
		assert.Equal(t, []string{"a-result.json"}, dirEntries(t, resultsDir))
		assert.Equal(t, `{"uuid":"second"}`, string(readFileT(t, filepath.Join(resultsDir, "a-result.json"))))
	})

	t.Run("rejects multipart without a boundary", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")
		body, _ := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

		w := do(s, "demo", body, "multipart/form-data")

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	})

	t.Run("ignores parts sent under another field name", func(t *testing.T) {
		s, _ := newTestServer(t, "demo")
		body, ct := multipartBody(t, uploadFile{"wrong", "a-result.json", `{"uuid":"a"}`})

		w := do(s, "demo", body, ct)

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	})
}

// Each case is a way an upload must not corrupt the results directory: a
// half-written file visible under its final name, a scratch file left behind,
// a planted symlink, an empty re-post over a good result, two jobs writing one
// fixed name at once.
func TestSavePart(t *testing.T) {
	t.Run("writes the whole stream", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		require.NoError(t, err)
		defer func() { _ = root.Close() }()

		n, err := savePart(root, "x.json", strings.NewReader("hello"))
		require.NoError(t, err)
		assert.EqualValues(t, 5, n)
		assert.Equal(t, "hello", string(readFileT(t, filepath.Join(root.Name(), "x.json"))))
	})

	t.Run("removes the file when the source fails", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		defer func() { _ = root.Close() }()

		src := io.MultiReader(strings.NewReader("partial"), errReader{})

		_, err = savePart(root, "x.json", src)
		require.Error(t, err)

		assert.Empty(t, dirEntries(t, dir), "neither the partial file nor its temporary may be kept")
	})

	t.Run("leaves no temporary behind once the file is published", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		defer func() { _ = root.Close() }()

		_, err = savePart(root, "x.json", strings.NewReader("hello"))
		require.NoError(t, err)

		assert.Equal(t, []string{"x.json"}, dirEntries(t, dir))
	})

	t.Run("publishes the file only once all of it is there", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		defer func() { _ = root.Close() }()

		var seenEarly bool
		src := io.MultiReader(
			strings.NewReader(`{"uuid":`),
			&hookReader{r: strings.NewReader(`"a"}`), fn: func() {
				if _, err := os.Stat(filepath.Join(dir, "x.json")); err == nil {
					seenEarly = true
				}
			}},
		)

		_, err = savePart(root, "x.json", src)
		require.NoError(t, err)

		assert.False(t, seenEarly, "x.json was visible under its final name while still half-written")
		assert.Equal(t, `{"uuid":"a"}`, string(readFileT(t, filepath.Join(dir, "x.json"))))
	})

	t.Run("does not write through a symlink out of root", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "escaped.json")
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "evil.json")))

		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		defer func() { _ = root.Close() }()

		_, err = savePart(root, "evil.json", strings.NewReader("x"))
		require.NoError(t, err)

		_, err = os.Stat(outside)
		assert.ErrorIs(t, err, os.ErrNotExist)

		fi, err := os.Lstat(filepath.Join(dir, "evil.json"))
		require.NoError(t, err)
		assert.Zero(t, fi.Mode()&os.ModeSymlink, "evil.json is still a symlink pointing out of the root")
	})

	t.Run("does not write through a symlink planted under a scratch name", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "escaped.json")

		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "evil.json.part")))

		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		defer func() { _ = root.Close() }()

		_, err = savePart(root, "evil.json", strings.NewReader("x"))
		require.NoError(t, err)
		_, err = os.Stat(outside)
		assert.ErrorIs(t, err, os.ErrNotExist)
		assert.Equal(t, "x", string(readFileT(t, filepath.Join(dir, "evil.json"))))
	})

	t.Run("an empty part leaves the file already on disk alone", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "x.json"), []byte("good"), 0o644))

		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		defer func() { _ = root.Close() }()

		n, err := savePart(root, "x.json", strings.NewReader(""))
		require.NoError(t, err)
		require.Zero(t, n)

		assert.Equal(t, "good", string(readFileT(t, filepath.Join(dir, "x.json"))))
		assert.Equal(t, []string{"x.json"}, dirEntries(t, dir))
	})

	t.Run("concurrent parts of one name do not interleave", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		defer func() { _ = root.Close() }()

		a := strings.Repeat("a", 64<<10)
		b := strings.Repeat("b", 64<<10)

		var wg sync.WaitGroup
		for _, content := range []string{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := savePart(root, "environment.properties", strings.NewReader(content))
				assert.NoError(t, err)
			}()
		}
		wg.Wait()

		got := string(readFileT(t, filepath.Join(dir, "environment.properties")))
		assert.True(t, got == a || got == b, "environment.properties is neither upload whole (len %d)", len(got))
		assert.Equal(t, []string{"environment.properties"}, dirEntries(t, dir))
	})
}

func readFileT(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

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

		err := fmt.Errorf("copy data: %w", &http.MaxBytesError{Limit: 1024})

		require.True(t, handleMaxBytesError(w, err))
		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
		assert.Contains(t, w.Body.String(), "1024")
	})

	t.Run("ignores other errors and writes nothing", func(t *testing.T) {
		w := httptest.NewRecorder()

		require.False(t, handleMaxBytesError(w, io.ErrUnexpectedEOF))
		assert.Empty(t, w.Body.String())
	})
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines      []time.Time
	writeDeadlines []time.Time
	err            error
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	if d.err != nil {
		return d.err
	}
	d.deadlines = append(d.deadlines, t)
	return nil
}

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

// Every read pushes the deadline idle ahead of itself, and a deadline never
// moves backwards.
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

		reads := 0
		buf := make([]byte, 2)
		for {
			_, err := body.Read(buf)
			reads++
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
		}

		require.Equal(t, 4, reads)
		require.Len(t, rec.deadlines, reads, "want one deadline per read")

		for i, d := range rec.deadlines {
			assert.False(t, d.Before(before.Add(idle)), "deadline %d = %v, want at least %v ahead", i, d, idle)
			if i > 0 {
				assert.False(t, d.Before(rec.deadlines[i-1]), "deadline %d moved backwards", i)
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
		require.ErrorIs(t, err, http.ErrNotSupported)
		assert.Zero(t, n)
		assert.Equal(t, 6, src.Len(), "the source must stay untouched")
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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.FileExists(t, filepath.Join(projects.ResultsDir(dir, "demo"), "a-result.json"))
	assert.GreaterOrEqual(t, len(rec.deadlines), 2, "want the probe plus at least one per-read refresh")
}

// The server's WriteTimeout counts from the request headers, so it covers the
// upload as well as the reply. Unless the handler lifts it, a long upload
// lands on disk and the client is told nothing.
func TestSendResultsLiftsTheWriteDeadline(t *testing.T) {
	s, _ := newTestServer(t, "demo")
	body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

	r := httptest.NewRequest(http.MethodPost, "/projects/demo/results", body)
	r.SetPathValue("id", "demo")
	r.Header.Set("Content-Type", ct)

	before := time.Now()
	rec := newDeadlineRecorder()
	s.sendResults(rec, r)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Len(t, rec.writeDeadlines, 1)
	assert.False(t, rec.writeDeadlines[0].Before(before.Add(uploadWriteDeadline)),
		"write deadline = %v, want uploadWriteDeadline out from the start", rec.writeDeadlines[0])
}

// A project deleted while its upload is being read gets exactly one 404, not a
// second error glued onto a response already sent.
func TestSendResultsProjectDeletedMidUpload(t *testing.T) {
	s, dir := newTestServer(t, "demo")
	body, ct := multipartBody(t, uploadFile{"files[]", "a-result.json", `{"uuid":"a"}`})

	hooked := &hookReader{r: body, fn: func() {
		assert.NoError(t, os.RemoveAll(filepath.Join(dir, "demo")))
	}}

	w := do(s, "demo", hooked, ct)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Equal(t, "project not found\n", w.Body.String())
}
