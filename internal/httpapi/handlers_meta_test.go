package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
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
			KeepHistory:              true,
			KeepHistoryLatest:        60,
			CheckResultsEverySeconds: 30,
		})

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
		// float64 literals. 30 rather than 3e10 is the point of the
		// assertion: a time.Duration field would publish nanoseconds.
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
		s := NewServer("unused-dir", nil, RuntimeConfig{})

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
		s := NewServer("unused-dir", nil, RuntimeConfig{KeepHistoryLatest: 7})

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
