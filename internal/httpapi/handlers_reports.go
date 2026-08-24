package httpapi

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

// generationStatusResponse is the JSON body returned by generationStatus. It
// restates report.Status instead of reusing it: that type carries an error,
// which encoding/json renders as an empty object, and the wire format should
// not shift every time the internal snapshot does.
type generationStatusResponse struct {
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
	// omitzero rather than omitempty: a struct is never "empty" to
	// encoding/json, so omitempty would publish the zero time as
	// 0001-01-01T00:00:00Z while the build is still running.
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Error      string    `json:"error,omitempty"`
}

// startGeneration handles POST /projects/{id}/generation. It asks the report
// generator to begin a build and answers as soon as the build has been
// accepted, so a 202 means the report is being produced, not that it is ready:
// a build takes minutes, far longer than any client or proxy will hold a
// request open. Callers learn the outcome by polling the generation endpoint.
//
// Responds 202 with no body on success, 400 if id fails
// projects.ValidateProjectID, 404 if the project has no results directory, and
// 409 if a build for that project is already in flight. The 409 is deliberate:
// the running build may have started before this caller uploaded its results,
// so reporting it as accepted would promise a report that never includes them.
// Any other failure is logged and reported as 500 without detail.
func (s *Server) startGeneration(w http.ResponseWriter, r *http.Request) {
	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	err := s.reports.Start(r.Context(), id)
	switch {
	case errors.Is(err, report.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
		return

	case errors.Is(err, report.ErrAlreadyRunning):
		http.Error(w, "report generation is already running", http.StatusConflict)
		return

	case errors.Is(err, report.ErrNoResults):
		http.Error(w, "project has no results to generate a report from", http.StatusConflict)
		return

	case err != nil:
		slog.Error("failed to start generation", "err", err, "project_id", id)
		http.Error(w, "failed to start generation", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// generationStatus handles GET /projects/{id}/generation. It reports where the
// last build started for the project stands. That record lives in memory, so a
// restart forgets it: a project whose report is sitting on disk can still
// answer 404 here.
//
// Responds 200 with generationStatusResponse, 400 if id fails
// projects.ValidateProjectID, and 404 if no build was ever started for the
// project. A build that failed is still a 200 — reading the status succeeded,
// and the failure belongs in the body as state "failed" plus the CLI's message
// in error.
func (s *Server) generationStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	st, ok := s.reports.Status(id)
	if !ok {
		http.Error(w, "no generation has been started for this project", http.StatusNotFound)
		return
	}

	resp := generationStatusResponse{
		State:      string(st.State),
		StartedAt:  st.StartedAt,
		FinishedAt: st.FinishedAt,
	}

	if st.Err != nil {
		resp.Error = st.Err.Error()
	}
	writeJSON(w, r, resp)
}

// clearHistory handles POST /projects/{id}/history/clean. Resets the
// project's trend history - its numbered archives and history.jsonl - and
// starts a fresh build, so a 202 here means the same thing it means on
// startGeneration: accepted, not yet finished. Callers learn the outcome by
// polling the generation endpoint, same as after startGeneration.
//
// Responds 400 if id fails projects.ValidateProjectID, 404 if the project
// has no results directory, 409 if a build for that project is already in
// flight or its results directory is empty, and 500 for any other failure.
func (s *Server) clearHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	liftWriteDeadline(w, lockWaitDeadline, id)

	err := s.reports.ClearHistory(r.Context(), id)
	switch {
	case errors.Is(err, report.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
		return
	case errors.Is(err, report.ErrNoResults):
		http.Error(w, "project has no results to clear history", http.StatusConflict)
		return
	case errors.Is(err, report.ErrAlreadyRunning):
		http.Error(w, "report generation is already running", http.StatusConflict)
		return
	case err != nil:
		slog.Error("failed to clear history", "err", err, "project_id", id)
		http.Error(w, "failed to clear history", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// exportReport handles GET /projects/{id}/report/export. It answers with the
// project's published report as a zip attachment, streamed straight into the
// response instead of being staged in memory or on disk first.
//
// Responds 200 with the archive, 400 if id fails projects.ValidateProjectID,
// 404 if the project has no published report, and 500 if that check itself
// fails. The existence check is a fast path to a clear 404, not a guarantee:
// the report can still be removed before the export takes the project's lock.
//
// Once the first byte of the archive is on the wire the status is fixed at
// 200, so a failure part-way through can only be logged - the client receives
// a truncated archive.
func (s *Server) exportReport(w http.ResponseWriter, r *http.Request) {
	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	_, err := os.Stat(projects.LatestReportDir(s.projectsDir, id))
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "report not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("failed to stat report directory", "err", err, "project_id", id)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}

	base := id + "-report"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+base+`.zip"`)

	liftWriteDeadline(w, exportWriteDeadline, id)

	err = s.reports.ExportLatest(id, w)
	if err != nil {
		slog.Error("failed to export report", "err", err, "project_id", id)
	}
}

// latestReport handles GET /projects/{id}/latest-report. It is a bookmarkable
// entry point to whatever the project has published: instead of serving
// anything itself it redirects to the report's static tree, which
// serveProjectReport then hands out.
//
// The target is relative - "reports/latest/" - so http.Redirect resolves it
// against the incoming request path. That keeps the Location correct whatever
// prefix the service is mounted under, which a hand-built absolute path would
// not. Both the trailing slash and the absent index.html matter: ServeFileFS
// canonicalises the URL itself, and either one missing costs the client a
// second redirect.
//
// The code is 302 rather than 301 deliberately. The target's existence depends
// on the disk - a report can be deleted or a project removed - and a permanent
// redirect is cached by browsers past any such change, with no way for the
// service to take it back.
//
// Responds 302 with the Location header, 400 if id fails
// projects.ValidateProjectID, 404 if the project has no published report, and
// 500 if that check itself fails. Like exportReport, the existence check is a
// fast path to a clear 404, not a guarantee: the report can be removed between
// the check and the client following the redirect.
func (s *Server) latestReport(w http.ResponseWriter, r *http.Request) {
	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	_, err := os.Stat(projects.LatestReportDir(s.projectsDir, id))
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "latest report not found", http.StatusNotFound)
		return
	}

	if err != nil {
		slog.Error("failed to stat latest report directory", "err", err, "project_id", id)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "reports/latest/", http.StatusFound)
}
