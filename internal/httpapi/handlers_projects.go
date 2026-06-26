package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

type createProjectRequest struct {
	ProjectID string `json:"project_id"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	errValidation := projects.ValidateProjectID(req.ProjectID)
	if errValidation != nil {
		http.Error(w, errValidation.Error(), http.StatusBadRequest)
		return
	}

	err = projects.CreateDir(s.projectsDir, req.ProjectID)
	switch {
	case errors.Is(err, projects.ErrProjectExists):
		http.Error(w, "project already exists", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	default:
		w.WriteHeader(http.StatusCreated)
	}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.projectsDir)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() {
			ids = append(ids, entry.Name())
		}

	}
}
