package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// msgInternalError is the body sent with every 500 in this package. It says
// nothing about the cause deliberately: the causes name filesystem paths and
// internal state, and belong in the log beside the request ID rather than in
// a response the client can read.
const msgInternalError = "internal server error"

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
// Handlers that need another status write it themselves before calling in; a
// status parameter would be the same value at every call site today.
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
