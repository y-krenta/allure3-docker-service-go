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

// versionResponse is the JSON body returned by getVersion. A bare JSON string
// would be a valid body too, but an object leaves room for the service's own
// version beside the CLI's, and matches the shape every other endpoint here
// answers with.
type versionResponse struct {
	Version string `json:"version"`
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

// getVersion handles GET /version. It reports the version of the Allure CLI
// this process builds reports with - asked of the binary itself at startup
// rather than read from an ALLURE_VERSION file, which describes what the image
// was built with and can disagree with what is actually installed.
//
// The value is read once in main and kept in memory, because it cannot change
// while the process runs: the binary is resolved at startup and never
// re-resolved. Shelling out per request would buy nothing and cost about
// 0.4s of JVM startup each time, which is also a free amplifier for anyone
// pointing a load generator at this endpoint.
//
// Responds 200 with versionResponse as JSON. There is no failure branch: a CLI
// that could not answer --version stopped the service at startup, so by the
// time this handler can be reached the string is there.
func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	resp := versionResponse{
		Version: s.allureVersion,
	}
	writeJSON(w, r, resp)
}
