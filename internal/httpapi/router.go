package httpapi

import (
	"net/http"
)

// Base paths for the route groups registered in Routes.
var (
	projectsEndpoint = "/projects"
	healthEndpoint   = "/health"
)

// Routes builds the HTTP handler for the whole service: it registers every
// route (see docs/10-api-usage.md for request/response examples) and wraps
// the mux with the recoverer, requestID and logger middleware, in that
// outer-to-inner order.
func (s *Server) Routes() http.Handler {
	r := http.NewServeMux()

	r.HandleFunc("GET "+healthEndpoint, s.healthCheck)
	r.HandleFunc("GET "+projectsEndpoint, s.listProjects)
	r.HandleFunc("GET "+projectsEndpoint+"/{id}", s.getProject)
	r.HandleFunc("GET "+projectsEndpoint+"/{id}/reports/{path...}", s.serveProjectReport)
	r.HandleFunc("POST "+projectsEndpoint, s.createProject)
	r.HandleFunc("DELETE "+projectsEndpoint+"/{id}", s.deleteProject)
	return recoverer(requestID(logger(r)))
}

// healthCheck reports service liveness. GET /health, no params, always 200.
func (s *Server) healthCheck(w http.ResponseWriter, _ *http.Request) {
	_, err := w.Write([]byte("service is ok\n"))
	if err != nil {
		return
	}

}
