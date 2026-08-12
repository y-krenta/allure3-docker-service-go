package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// createProjectRequest is the JSON body expected by createProject.
type createProjectRequest struct {
	ProjectID string `json:"project_id"`
}

// listProjectsResponse is the JSON body returned by listProjects.
type listProjectsResponse struct {
	Projects []string `json:"projects"`
}

// projectBuildsResponse is the JSON body returned by getProject.
type projectBuildsResponse struct {
	Builds []string `json:"builds"`
}

// createProject handles POST /projects. Body: createProjectRequest. On
// success responds 201 with no body. Responds 400 for an invalid body or a
// project_id that fails projects.ValidateProjectID, 409 if the project
// already exists.
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
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	default:
		w.WriteHeader(http.StatusCreated)
	}
}

// listProjects handles GET /projects. The optional "search" query param
// filters project IDs by a case-insensitive substring match. Responds 200
// with listProjectsResponse.
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	search := strings.ToLower(r.URL.Query().Get("search"))
	entries, err := os.ReadDir(s.projectsDir)
	if err != nil {
		slog.Error("error read dir", "err", err)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
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
	writeJSON(w, r, resp)
}

// deleteProject handles DELETE /projects/{id}. Removes the project
// directory recursively. Responds 204 on success, 400 if id fails
// projects.ValidateProjectID, 403 for the reserved default project
// (projects.DefaultProjectID).
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	if id == projects.DefaultProjectID {
		slog.Error("It is forbidden to delete the default directory")
		http.Error(w, "The default directory cannot be deleted.", http.StatusForbidden)
		return
	}
	err := os.RemoveAll(filepath.Join(s.projectsDir, id))
	if err != nil {
		slog.Error("failed to delete project", "err", err, "project_id", id)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getProject handles GET /projects/{id}. Lists the project's builds: report
// subdirectories that contain an index.html, sorted by that file's mtime
// descending, with "latest" always pinned first. Responds 200 with
// projectBuildsResponse, 400 if id fails projects.ValidateProjectID, 404 if
// the project directory doesn't exist.
func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	// buildEntry pairs a build directory name with its index.html mtime,
	// used only to sort builds before the response is assembled.
	type buildEntry struct {
		Name string
		When time.Time
	}

	var items []buildEntry

	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	projectPath := filepath.Join(s.projectsDir, id)
	_, errStat := os.Stat(projectPath)
	if errors.Is(errStat, os.ErrNotExist) {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	} else if errStat != nil {
		slog.Error("failed to stat project path", "err", errStat, "path", projectPath)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}
	builds := make([]string, 0)

	buildsPath := projects.ReportsDir(s.projectsDir, id)
	entries, err := os.ReadDir(buildsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("failed to read dir", "err", err, "path", buildsPath)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pathToIndex := filepath.Join(buildsPath, entry.Name(), "index.html")
		info, errStatIndex := os.Stat(pathToIndex)
		if errStatIndex != nil {
			if errors.Is(errStatIndex, os.ErrNotExist) {
				continue
			}
			slog.Error("failed to stat index file", "err", errStatIndex, "path", pathToIndex)
			http.Error(w, msgInternalError, http.StatusInternalServerError)
			return
		}
		items = append(items, buildEntry{Name: entry.Name(), When: info.ModTime()})

	}
	cmp := func(a, b buildEntry) int {
		return b.When.Compare(a.When)
	}
	slices.SortFunc(items, cmp)
	for _, item := range items {
		if item.Name == "latest" {
			builds = append([]string{item.Name}, builds...)
		} else {
			builds = append(builds, item.Name)
		}

	}

	resp := projectBuildsResponse{Builds: builds}
	writeJSON(w, r, resp)

}

func (s *Server) serveProjectReport(w http.ResponseWriter, r *http.Request) {
	id, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	reportPath := r.PathValue("path")
	reportPath = path.Clean(reportPath)
	if reportPath == "." {
		http.Error(w, "path empty", http.StatusBadRequest)
		return
	}

	rootDir := os.DirFS(projects.ReportsDir(s.projectsDir, id))
	http.ServeFileFS(w, r, rootDir, reportPath)

}
