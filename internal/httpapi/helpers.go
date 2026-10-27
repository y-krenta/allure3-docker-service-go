package httpapi

import (
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

const (
	// msgInternalError is the body sent with every 500 in this package. It says
	// nothing about the cause deliberately: the causes name filesystem paths and
	// internal state, and belong in the log beside the request ID rather than in
	// a response the client can read.
	msgInternalError = "internal server error"
	// exportWriteDeadline is how long the export handler gives itself to
	// finish writing, replacing the server's WriteTimeout for that one
	// response through http.ResponseController. It has to cover both the
	// wait for a running build to release the project's lock - bounded by
	// the report package's own generate timeout - and the streaming of the
	// archive afterwards, neither of which fits the seconds-long budget
	// every other handler shares.
	exportWriteDeadline = 15 * time.Minute

	// uploadWriteDeadline is how long the upload handler gives itself to
	// finish, replacing the server's WriteTimeout for that one response
	// through http.ResponseController. The handler is built for uploads that
	// run for minutes - a gigabyte cap, and an idle timeout between reads
	// rather than one over the whole request - and the server's WriteTimeout
	// is counted from the moment the request headers are read, so it covers
	// the upload as well as the reply. Without this the files of a long
	// upload all land on disk and the response saying so cannot be written:
	// the client sees a broken connection and calls a successful upload
	// failed.
	uploadWriteDeadline = 15 * time.Minute

	// lockWaitDeadline replaces the server's WriteTimeout for the handlers
	// that take a project's lock and can therefore be left waiting for a build
	// in flight: clearing results, clearing history and deleting a project.
	// A build is bounded by the report package's own generate timeout of ten
	// minutes, forty times the server-wide WriteTimeout, so without this the
	// wait outlives the connection the answer has to go out on: the work is
	// done, the project is cleared or deleted, and the caller sees a broken
	// connection instead of its 204 and retries an operation that already
	// succeeded.
	lockWaitDeadline = 15 * time.Minute
)

// liftWriteDeadline pushes this response's write deadline out to d, replacing
// the server-wide WriteTimeout for this one request. Failure is logged and
// otherwise ignored: the handler can still do its work, it only risks losing
// the reply, and refusing the request outright would be a worse answer than
// the one the caller is already waiting for.
func liftWriteDeadline(w http.ResponseWriter, d time.Duration, projectID string) {
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
	if err != nil {
		slog.Error("failed to set write deadline", "err", err, "project_id", projectID)
	}
}

// unwrapResponseWriter peels the middleware wrappers off w and returns the
// ResponseWriter underneath them all.
//
// It exists for http.MaxBytesReader, which tells the server to stop reading an
// oversized request through a bare type assertion on the writer it was handed
// - no Unwrap walk, unlike http.ResponseController - so handing it the
// logger middleware's statusRecorder silently disables that: the 413 is
// written, the connection is not marked for close, and the server goes on
// trying to drain a body the client is still sending.
func unwrapResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	for {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = u.Unwrap()
	}
}

// requireProjectID reads the {id} path value and validates it, returning the
// project ID and whether it is usable.
//
// When it reports false it has already answered the request with 400 and the
// validation message, and the caller must return without writing anything
// more: a second write would append to a response that has been sent, and
// net/http would log the extra header as a superfluous WriteHeader call.
func requireProjectID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	err := projects.ValidateProjectID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	return id, true
}

// writeJSON answers the request with v encoded as JSON, under an implicit 200.
// It cannot carry any other status: a WriteHeader before the call sends the
// headers, and the Content-Type set here would be silently dropped. An error
// goes out through writeError instead.
//
// v is encoded straight into w rather than buffered first, so by the time
// encoding can fail part of the body has been sent and the status can no
// longer be changed. Failure is therefore only logged, with the request path
// to say which endpoint went quiet mid-response. Buffering would let a broken
// value be reported as a 500 instead, but the only values passed here are
// strings, times and slices of them, which encoding/json cannot refuse.
func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode response", "err", err, "path", r.URL.Path)
	}
}

// writeError answers the request with status and the JSON body
// {"error": msg}. msg is read by the client, so it names what the client did
// wrong and never an internal cause. Encoding failure is only logged, as in
// writeJSON.
func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		slog.Error("failed to encode response", "err", err, "path", r.URL.Path)
	}
}

// readJSON decodes the request body into v, which must be a pointer, and
// reports whether it did. A body not declared as application/json is a 415
// before anything is read, one over limit bytes a 413 read no further, and
// one that is not the JSON v expects a 400.
//
// When it reports false it has already answered the request, and the caller
// must return without writing anything more, as with requireProjectID.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, r, http.StatusUnsupportedMediaType, "content type must be application/json")
		return false
	}

	r.Body = http.MaxBytesReader(unwrapResponseWriter(w), r.Body, limit)

	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if !handleMaxBytesError(w, err) {
			http.Error(w, "invalid request body", http.StatusBadRequest)
		}
		return false
	}
	return true
}
