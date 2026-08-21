package httpapi

import "net/http"

// configResponse is the JSON body returned by getConfig. It exists apart from
// RuntimeConfig to keep one conversion inside this package, where a test can
// reach it: the interval is held as a time.Duration, which encoding/json would
// render as its raw int64 of nanoseconds, publishing 30000000000 under a name
// that promises seconds. Turning it into seconds in main instead would put
// that line in the one package with no tests at all.
type configResponse struct {
	KeepHistory              bool `json:"keep_history"`
	KeepHistoryLatest        int  `json:"keep_history_latest"`
	CheckResultsEverySeconds int  `json:"check_results_every_seconds"`
}

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
// Responds 200 with configResponse as JSON.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	resp := configResponse{
		KeepHistory:              s.cfg.KeepHistory,
		KeepHistoryLatest:        s.cfg.KeepHistoryLatest,
		CheckResultsEverySeconds: int(s.cfg.CheckResultsEvery.Seconds()),
	}
	writeJSON(w, r, resp)
}
