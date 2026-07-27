package httpapi

// Server holds the shared dependencies for HTTP handlers, such as the base
// directory under which per-project results/reports are stored.
type Server struct {
	projectsDir string
}

// NewServer builds a Server that resolves project storage under projectsDir.
func NewServer(projectsDir string) *Server {
	return &Server{projectsDir: projectsDir}
}
