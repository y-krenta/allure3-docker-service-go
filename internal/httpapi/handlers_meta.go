package httpapi

import "net/http"

// getConfig handles GET /config. It answers with the settings the service is
// actually running under, so a client can ask how the service will behave
// instead of assuming: whether trend history is kept and how deep, and how
// often the watcher picks up results on its own.
//
// The body is RuntimeConfig, whose doc explains why it is a chosen subset of
// the configuration rather than all of it. The endpoint is global, not
// project-scoped - these settings are process-wide - and it takes no
// parameters, touches no disk and has no failure mode of its own, which is
// why it is the only handler here without an error branch.
//
// Responds 200 with RuntimeConfig as JSON.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.cfg)
}
