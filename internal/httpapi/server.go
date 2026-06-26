package httpapi

type Server struct {
	projectsDir string
}

func NewServer(projectsDir string) *Server {
	return &Server{projectsDir: projectsDir}
}
