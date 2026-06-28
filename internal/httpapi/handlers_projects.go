package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

type createProjectRequest struct {
	ProjectID string `json:"project_id"`
}

type listProjectsResponse struct {
	Projects []string `json:"projects"`
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
	w.Header().Set("Content-Type", "application/json")
	entries, err := os.ReadDir(s.projectsDir)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	ids := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}

	}

	resp := listProjectsResponse{Projects: ids}
	encoder := json.NewEncoder(w)
	errEncoding := encoder.Encode(resp)
	if errEncoding != nil {
		slog.Error("failed to encode response: ", "error", errEncoding)
		return
	}
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	errValidation := projects.ValidateProjectID(id)
	if errValidation != nil {
		http.Error(w, errValidation.Error(), http.StatusBadRequest)
		return
	}
	err := os.RemoveAll(filepath.Join(s.projectsDir, id))
	if err != nil {
		slog.Error("failed to delete project", "error", err, "project_id", id)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
