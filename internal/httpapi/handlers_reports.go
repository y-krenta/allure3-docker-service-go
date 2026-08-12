package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

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
	id := r.PathValue("id")
	err := projects.ValidateProjectID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = s.reports.Start(r.Context(), id)
	switch {
	case errors.Is(err, report.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
		return

	case errors.Is(err, report.ErrAlreadyRunning):
		http.Error(w, "report generation is already running", http.StatusConflict)
		return

	case err != nil:
		slog.Error("failed to start generation", "err", err, "project_id", id)
		http.Error(w, "failed to start generation", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}
