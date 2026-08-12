package httpapi

import (
	"net/http"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// requireProjectID reads the {id} path value and validates it, returning the
// project ID and whether it is usable.
//
// When it reports false it has already answered the request with 400 and the
// validation message, and the caller must return without writing anything
// more: a second write would append to a response that has been sent, and
// net/http would log the extra header as a superfluous WriteHeader call.
func requireProjectID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	err := projects.ValidateProjectID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	return id, true
}
