package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

type createProjectRequest struct {
	ProjectID string `json:"project_id"`
}

type listProjectsResponse struct {
	Projects []string `json:"projects"`
}

type projectBuildsResponse struct {
	Builds []string `json:"builds"`
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
	search := strings.ToLower(r.URL.Query().Get("search"))
	entries, err := os.ReadDir(s.projectsDir)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	ids := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			name := strings.ToLower(entry.Name())
			if strings.Contains(name, search) {
				ids = append(ids, entry.Name())
			}
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

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	errValidation := projects.ValidateProjectID(id)
	if errValidation != nil {
		http.Error(w, errValidation.Error(), http.StatusBadRequest)
		return
	}
	projectPath := filepath.Join(s.projectsDir, id)
	_, errStat := os.Stat(projectPath)
	if errors.Is(errStat, os.ErrNotExist) {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	} else if errStat != nil {
		slog.Error("failed to stat project path", "error", errStat, "path", projectPath)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	builds := make([]string, 0)

	buildsPath := projects.ReportsDir(s.projectsDir, id)
	entries, err := os.ReadDir(buildsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("failed to read dir", "error", err, "path", buildsPath)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			builds = append(builds, entry.Name())
		}
	}

	w.Header().Set("Content-Type", "application/json")
	resp := projectBuildsResponse{Builds: builds}

	err = json.NewEncoder(w).Encode(resp)
	if err != nil {
		slog.Error("failed to encode response", "error", err)
		return
	}

}
