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

// versionResponse is the JSON body returned by getVersion. It carries both
// versions that describe a running container: the Allure CLI that builds the
// reports, and the service that drives it.
//
// Each key names what it holds. In 0.0.1 the endpoint answered
// {"version": "3.15.0"}, where version meant the CLI's - and keeping that key
// while adding service_version beside it would have frozen the vaguer name
// onto somebody else's version for good. Renaming it breaks any client of
// 0.0.1 that read version, a debt worth paying while the release is days old
// rather than years.
type versionResponse struct {
	AllureVersion  string `json:"allure_version"`
	ServiceVersion string `json:"service_version"`
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

// getVersion handles GET /version. It reports two versions: the Allure CLI
// this process builds reports with, and the service itself.
//
// The CLI's is asked of the binary at startup rather than read from an
// ALLURE_VERSION file, which describes what the image was built with and can
// disagree with what is actually installed. The service's is stamped into this
// binary at link time, which is the same argument from the other side: a
// version living anywhere but inside the executable can drift away from the
// code it claims to name.
//
// Both are read once in main and kept in memory, because neither can change
// while the process runs: the CLI is resolved at startup and never
// re-resolved, and the stamped string is part of the executable. Shelling out
// per request would buy nothing and cost about 0.2s of Node startup each time,
// which is also a free amplifier for anyone pointing a load generator at this
// endpoint.
//
// Responds 200 with versionResponse as JSON. There is no failure branch: a CLI
// that could not answer --version stopped the service at startup, and the
// service's own version is a compile-time value in all but name, so by the
// time this handler can be reached both strings are there.
func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	resp := versionResponse{
		AllureVersion:  s.versions.Allure,
		ServiceVersion: s.versions.Service,
	}
	writeJSON(w, r, resp)
}
