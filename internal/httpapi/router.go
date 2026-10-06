package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Base paths for the route groups registered in Routes.
const (
	projectsEndpoint = "/projects"
	healthEndpoint   = "/health"
	configEndpoint   = "/config"
	versionEndpoint  = "/version"
)

// Routes builds the HTTP handler for the whole service: it registers every
// route (see the HTTP API section of README.md for request/response examples)
// and wraps the mux with the recoverer, requestID and logger middleware, in
// that outer-to-inner order.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.CleanPath, middleware.GetHead)

	r.Get(healthEndpoint, s.healthCheck)
	r.Get(projectsEndpoint, s.listProjects)
	r.Get(configEndpoint, s.getConfig)
	r.Get(versionEndpoint, s.getVersion)
	r.Get(projectsEndpoint+"/{id}", s.getProject)
	r.Get(projectsEndpoint+"/{id}/reports/*", s.serveProjectReport)
	r.Get(projectsEndpoint+"/{id}/generation", s.generationStatus)
	r.Get(projectsEndpoint+"/{id}/report/export", s.exportReport)
	r.Get(projectsEndpoint+"/{id}/latest-report", s.latestReport)

	r.Post(projectsEndpoint, s.createProject)
	r.Post(projectsEndpoint+"/{id}/results", s.sendResults)
	r.Post(projectsEndpoint+"/{id}/generation", s.startGeneration)
	r.Post(projectsEndpoint+"/{id}/history/clean", s.clearHistory)
	r.Post(projectsEndpoint+"/{id}/history/seed", s.seedHistory)

	r.Delete(projectsEndpoint+"/{id}", s.deleteProject)
	r.Delete(projectsEndpoint+"/{id}/results", s.clearResults)
	return recoverer(requestID(logger(r)))
}

// healthCheck reports service liveness. GET /health, no params, always 200.
func (s *Server) healthCheck(w http.ResponseWriter, _ *http.Request) {
	_, err := w.Write([]byte("service is ok\n"))
	if err != nil {
		return
	}

}
