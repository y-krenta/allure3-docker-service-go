package httpapi

import (
	"net/http"
)

var (
	projectsEndpoint = "/projects"
	healthEndpoint   = "/health"
)

func (s *Server) Routes() http.Handler {
	r := http.NewServeMux()

	r.HandleFunc("GET "+healthEndpoint, s.healthCheck)
	r.HandleFunc("GET "+projectsEndpoint, s.listProjects)
	r.HandleFunc("POST "+projectsEndpoint, s.createProject)
	r.HandleFunc("DELETE "+projectsEndpoint+"/{id}", s.deleteProject)
	return recoverer(logger(r))
}

func (s *Server) healthCheck(w http.ResponseWriter, _ *http.Request) {
	_, err := w.Write([]byte("service is ok\n"))
	if err != nil {
		return
	}

}
