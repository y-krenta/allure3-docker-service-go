package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v3"

	"github.com/y-krenta/allure3-docker-service-go/internal/auth"
)

// Base paths for the route groups registered in Routes.
const (
	projectsEndpoint = "/projects"
	healthEndpoint   = "/health"
	configEndpoint   = "/config"
	versionEndpoint  = "/version"
	loginEndpoint    = "/auth/login"
	refreshEndpoint  = "/auth/refresh"
)

// Routes builds the HTTP handler for the whole service: it registers every
// route (see the HTTP API section of README.md for request/response examples)
// behind this middleware, outer to inner:
//
//   - httplog writes one log line per request and turns a panic into 500;
//   - requestID adds the request ID to that line, which only works from
//     inside httplog;
//   - CleanPath routes on the cleaned path, so "//projects" still matches;
//   - GetHead answers HEAD on GET routes. It must come after CleanPath, which
//     skips cleaning once GetHead has set the route path.
//
// The login and refresh routes exist only on a Server that EnableAuth was
// called on. Without auth they answer 404 like any unknown path, rather than
// reaching a handler with no users to check against. Both sit outside the
// role groups: login has no token yet, and refresh carries a refresh token,
// which requireRole would refuse as not an access one.
//
// With auth, every other route sits in one of two groups. Every GET but
// health is for a viewer or an admin; every POST and DELETE, being all that
// changes data, for an admin alone. With public reads the GET group carries
// no middleware, so a token sent there is not even read. Without auth neither
// group does and every route is open.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(
		httplog.RequestLogger(slog.Default(), &httplog.Options{
			RecoverPanics: true,
			Schema:        httplog.SchemaECS,
		}),
		requestID,
		middleware.CleanPath,
		middleware.GetHead,
	)

	r.Get(healthEndpoint, s.healthCheck)
	if s.users != nil {
		r.Post(loginEndpoint, s.login)
		r.Post(refreshEndpoint, s.refresh)
	}

	r.Group(func(r chi.Router) {
		if s.tokens != nil && !s.publicReads {
			r.Use(s.requireRole(auth.RoleAdmin, auth.RoleViewer))
		}
		r.Get(projectsEndpoint, s.listProjects)
		r.Get(configEndpoint, s.getConfig)
		r.Get(versionEndpoint, s.getVersion)
		r.Get(projectsEndpoint+"/{id}", s.getProject)
		r.Get(projectsEndpoint+"/{id}/reports/*", s.serveProjectReport)
		r.Get(projectsEndpoint+"/{id}/generation", s.generationStatus)
		r.Get(projectsEndpoint+"/{id}/report/export", s.exportReport)
		r.Get(projectsEndpoint+"/{id}/latest-report", s.latestReport)
	})

	r.Group(func(r chi.Router) {
		if s.tokens != nil {
			r.Use(s.requireRole(auth.RoleAdmin))
		}
		r.Post(projectsEndpoint, s.createProject)
		r.Post(projectsEndpoint+"/{id}/results", s.sendResults)
		r.Post(projectsEndpoint+"/{id}/generation", s.startGeneration)
		r.Post(projectsEndpoint+"/{id}/history/clean", s.clearHistory)
		r.Post(projectsEndpoint+"/{id}/history/seed", s.seedHistory)

		r.Delete(projectsEndpoint+"/{id}", s.deleteProject)
		r.Delete(projectsEndpoint+"/{id}/results", s.clearResults)
	})

	return r
}

// healthCheck reports service liveness. GET /health, no params, always 200.
func (s *Server) healthCheck(w http.ResponseWriter, _ *http.Request) {
	_, err := w.Write([]byte("service is ok\n"))
	if err != nil {
		return
	}

}
