package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

var (
	projectsDir    = "/projects"
	healthEndpoint = "/health"
)

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
	r.Get(healthEndpoint, s.healthCheck)
	r.Get(projectsDir, s.listProjects)
	r.Post(projectsDir, s.createProject)
	return r
}

func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	_, err := w.Write([]byte("service is ok\n"))
	if err != nil {
		return
	}

}
