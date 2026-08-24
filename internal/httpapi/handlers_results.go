package httpapi

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// sendResultsResponse describes a successful upload response.
type sendResultsResponse struct {
	Files []string `json:"processed_files"`
	Count int      `json:"processed_files_count"`
}

// idleTimeoutBody wraps a request body and pushes the connection's read
// deadline forward on every read, so the deadline bounds the pause between
// chunks rather than the whole upload: a slow but progressing client keeps
// going, a client that goes silent is cut off after idle. Callers must only
// wrap the body once the ResponseWriter is known to support read deadlines,
// otherwise every read fails.
type idleTimeoutBody struct {
	io.ReadCloser
	rc   *http.ResponseController
	idle time.Duration
}

// Read moves the read deadline idle into the future and then reads from the
// wrapped body, passing its result through unchanged.
func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	err := b.rc.SetReadDeadline(time.Now().Add(b.idle))
	if err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}

// sendResults accepts multipart Allure result files for a project and stores them
// in the project's results directory.
func (s *Server) sendResults(w http.ResponseWriter, r *http.Request) {
	const (
		maxUploadBytes  = 1 << 30 // 1 GB
		readIdleTimeout = 60 * time.Second
	)
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	rc := http.NewResponseController(w)

	err := rc.SetReadDeadline(time.Now().Add(readIdleTimeout))
	switch {
	case err == nil:
		r.Body = &idleTimeoutBody{ReadCloser: r.Body, rc: rc, idle: readIdleTimeout}

	case errors.Is(err, http.ErrNotSupported):
		slog.Warn("read deadlines not supported, uploading without idle timeout", "err", err)

	default:
		slog.Error("failed to set read deadline", "err", err)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}

	err = rc.SetWriteDeadline(time.Now().Add(uploadWriteDeadline))
	if err != nil {
		slog.Error("failed to set write deadline", "err", err)
	}

	ct := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "multipart/form-data" {
		http.Error(w, "invalid content type", http.StatusUnsupportedMediaType)
		return
	}

	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	resultsPath := projects.ResultsDir(s.projectsDir, id)

	root, err := os.OpenRoot(resultsPath)
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("failed to open project results dir", "err", err, "path", resultsPath)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()

	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "failed to read multipart form data", http.StatusBadRequest)
		return
	}

	files := make([]string, 0)

	// upload is not transactional; files saved before an error remain on disk.
	// This is acceptable because Allure result filenames are UUID-based and retries overwrite
	// the same files instead of creating duplicates.
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if handleMaxBytesError(w, err) {
				return
			}
			http.Error(w, "failed to read multipart part", http.StatusBadRequest)
			return
		}

		if part.FormName() != "files[]" || part.FileName() == "" {
			continue
		}

		name, err := projects.SanitizeResultFileName(part.FileName())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		n, err := savePart(root, name, part)
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		if err != nil {
			if handleMaxBytesError(w, err) {
				return
			}

			slog.Error("failed to save uploaded file", "err", err, "file", name)
			http.Error(w, msgInternalError, http.StatusInternalServerError)
			return
		}

		if n == 0 {
			if err := root.Remove(name); err != nil {
				slog.Error("failed to remove empty uploaded file", "err", err, "file", name)
			}
			continue
		}

		files = append(files, name)
	}

	if len(files) == 0 {
		http.Error(w, "no files uploaded", http.StatusBadRequest)
		return
	}

	resp := sendResultsResponse{
		Files: files,
		Count: len(files),
	}

	writeJSON(w, r, resp)
}

// handleMaxBytesError writes 413 Payload Too Large if err contains
// *http.MaxBytesError and reports whether the error was handled.
func handleMaxBytesError(w http.ResponseWriter, err error) bool {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		msg := fmt.Sprintf("upload exceeds %d bytes", maxErr.Limit)
		http.Error(w, msg, http.StatusRequestEntityTooLarge)
		return true
	}
	return false
}

// savePart streams src into a file called name under root and reports how many
// bytes arrived. On any error it removes what it had written and the file never
// appears under name at all.
//
// The data goes to a scratch name first and is renamed into place only once all
// of it is there. Copying straight into name would publish the file the moment
// it was created and fill it in afterwards, leaving a window in which anything
// reading the results directory - the Allure CLI during a build, the watcher
// fingerprinting it - could pick up a half-written result and take it for a
// malformed one. A rename within one directory is atomic: a reader sees the
// whole file or no file, never part of it.
//
// The scratch name is name with a suffix, so it is as guessable as name itself
// and needs the same protection. It gets it: both go through root, which
// refuses either one if it resolves outside - see the tests, which plant a
// symlink under each name in turn.
//
// A symlink already sitting at name is replaced rather than followed, because
// rename acts on the link itself and not on what it points at. That differs
// from copying into name directly, which os.Root refuses outright, but only in
// what the call answers: either way nothing is written outside root, and here
// the link is left neutralised instead of still aimed out of it.
//
// A missing directory surfaces as an error wrapping fs.ErrNotExist, which is
// what a project deleted mid-upload looks like from in here: root stays open on
// a directory no longer attached to anything, and creating in it fails.
func savePart(root *os.Root, name string, src io.Reader) (int64, error) {
	tmp := name + ".part"

	f, err := root.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("failed to create file: %w", err)
	}

	ok := false
	defer func() {
		if ok {
			return
		}

		_ = f.Close()

		if err := root.Remove(tmp); err != nil {
			slog.Error("failed to remove partially written file", "err", err, "file", tmp)
		}
	}()

	n, err := io.Copy(f, src)
	if err != nil {
		return n, fmt.Errorf("failed to copy data into file: %w", err)
	}
	if err := f.Close(); err != nil {
		return n, fmt.Errorf("failed to close file: %w", err)
	}
	if err := root.Rename(tmp, name); err != nil {
		return n, fmt.Errorf("failed to publish file: %w", err)
	}

	ok = true
	return n, nil
}
