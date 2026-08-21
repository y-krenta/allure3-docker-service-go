package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGetConfig(t *testing.T) {
	// The body is decoded into a map rather than back into RuntimeConfig.
	// Unmarshalling into the struct would agree with itself whatever the
	// tags say: rename keep_history to keepHistory and a round-trip test
	// still passes, while every existing client breaks. The map sees the
	// wire, and the key set is asserted exactly - that is what catches a
	// field being added to RuntimeConfig that has no business leaving the
	// process, such as the projects directory.
	wantKeys := []string{"check_results_every_seconds", "keep_history", "keep_history_latest"}

	t.Run("reports the settings the server was built with", func(t *testing.T) {
		s := NewServer("unused-dir", nil, RuntimeConfig{
			KeepHistory:       true,
			KeepHistoryLatest: 60,
			CheckResultsEvery: 30 * time.Second,
		}, "")

		w := httptest.NewRecorder()
		s.getConfig(w, httptest.NewRequest(http.MethodGet, "/config", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding body %q: %v", w.Body.String(), err)
		}

		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, wantKeys) {
			t.Errorf("keys = %v, want %v", keys, wantKeys)
		}

		if got["keep_history"] != true {
			t.Errorf("keep_history = %v, want true", got["keep_history"])
		}
		// JSON numbers decode into float64, so the comparison is against
		// float64 literals. The interval is the assertion that matters:
		// the server holds 30 * time.Second, and 30 rather than 3e10 on
		// the wire is what says the Duration was converted rather than
		// marshalled as its raw nanosecond count.
		if got["keep_history_latest"] != float64(60) {
			t.Errorf("keep_history_latest = %v, want 60", got["keep_history_latest"])
		}
		if got["check_results_every_seconds"] != float64(30) {
			t.Errorf("check_results_every_seconds = %v, want 30", got["check_results_every_seconds"])
		}
	})

	t.Run("zero values are published, not omitted", func(t *testing.T) {
		// History off and the watcher disabled are the defaults on a
		// plain run. With omitempty on the tags they would vanish from
		// the body, and a client could not tell "history is off" from
		// "this service is too old to say".
		s := NewServer("unused-dir", nil, RuntimeConfig{}, "")

		w := httptest.NewRecorder()
		s.getConfig(w, httptest.NewRequest(http.MethodGet, "/config", nil))

		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding body %q: %v", w.Body.String(), err)
		}

		for _, k := range wantKeys {
			if _, ok := got[k]; !ok {
				t.Errorf("key %q missing from %s", k, w.Body.String())
			}
		}
	})

	t.Run("the route is registered", func(t *testing.T) {
		// getConfig is reached through Routes here, not called directly:
		// the handler can be perfect and the endpoint still 404 if the
		// pattern is wrong or the method is not GET.
		s := NewServer("unused-dir", nil, RuntimeConfig{KeepHistoryLatest: 7}, "")

		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/config", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", w.Code, http.StatusOK, w.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding body %q: %v", w.Body.String(), err)
		}
		if got["keep_history_latest"] != float64(7) {
			t.Errorf("keep_history_latest = %v, want 7", got["keep_history_latest"])
		}
	})
}

func TestGetVersion(t *testing.T) {
	// Same reason as TestGetConfig: the body is read as a map, not decoded
	// back into versionResponse, so the assertion is on the key that ships
	// rather than on the struct agreeing with itself.
	t.Run("reports the version the server was built with", func(t *testing.T) {
		s := NewServer("unused-dir", nil, RuntimeConfig{}, "3.14.3")

		w := httptest.NewRecorder()
		s.getVersion(w, httptest.NewRequest(http.MethodGet, "/version", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding body %q: %v", w.Body.String(), err)
		}
		if len(got) != 1 {
			t.Errorf("body has %d keys, want 1: %s", len(got), w.Body.String())
		}
		// Asserted whole, not by prefix: main trims the trailing newline
		// the CLI prints, and a version still carrying it would satisfy
		// a prefix or contains check while being wrong on the wire.
		if got["version"] != "3.14.3" {
			t.Errorf("version = %v, want 3.14.3", got["version"])
		}
	})

	t.Run("the route is registered", func(t *testing.T) {
		// Reached through Routes, so a pattern registered under the wrong
		// prefix - /projects/version, say, which also collides with the
		// {id} wildcard - shows up here rather than in production.
		s := NewServer("unused-dir", nil, RuntimeConfig{}, "3.14.3")

		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/version", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", w.Code, http.StatusOK, w.Body.String())
		}
		if got := strings.TrimSpace(w.Body.String()); got != `{"version":"3.14.3"}` {
			t.Errorf("body = %s, want {\"version\":\"3.14.3\"}", got)
		}
	})
}
