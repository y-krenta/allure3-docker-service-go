package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeleteProjectRemovesOnlyThatProject(t *testing.T) {
	c := newClient(t)
	gone, kept := projectID(t)+"-gone", projectID(t)+"-kept"

	c.createProject(gone)
	c.createProject(kept)
	c.run(gone, passed(1))
	c.run(kept, passed(1))

	c.do(c.request(http.MethodDelete, "/projects/"+gone, nil), http.StatusNoContent)

	var list struct {
		Projects []string `json:"projects"`
	}
	c.getJSON("/projects", &list)
	assert.NotContains(t, list.Projects, gone)
	assert.Contains(t, list.Projects, kept)

	for _, path := range []string{
		"/projects/" + gone,
		"/projects/" + gone + "/reports/latest/index.html",
		"/projects/" + gone + "/generation",
	} {
		c.do(c.request(http.MethodGet, path, nil), http.StatusNotFound)
	}

	c.get("/projects/" + kept + "/reports/latest/index.html")
	c.requireSucceeded(kept)
}
