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

	wantKeys := []string{"check_results_every_seconds", "keep_history", "keep_history_latest"}

	t.Run("reports the settings the server was built with", func(t *testing.T) {
		s := NewServer("unused-dir", nil, RuntimeConfig{
			KeepHistory:       true,
			KeepHistoryLatest: 60,
			CheckResultsEvery: 30 * time.Second,
		}, Versions{})

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

		if got["keep_history_latest"] != float64(60) {
			t.Errorf("keep_history_latest = %v, want 60", got["keep_history_latest"])
		}
		if got["check_results_every_seconds"] != float64(30) {
			t.Errorf("check_results_every_seconds = %v, want 30", got["check_results_every_seconds"])
		}
	})

	t.Run("zero values are published, not omitted", func(t *testing.T) {

		s := NewServer("unused-dir", nil, RuntimeConfig{}, Versions{})

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

		s := NewServer("unused-dir", nil, RuntimeConfig{KeepHistoryLatest: 7}, Versions{})

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
	const (
		allureVersion  = "3.14.3"
		serviceVersion = "0.0.2-test"
	)

	versions := Versions{Allure: allureVersion, Service: serviceVersion}

	t.Run("reports both versions the server was built with", func(t *testing.T) {
		s := NewServer("unused-dir", nil, RuntimeConfig{}, versions)

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

		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if want := []string{"allure_version", "service_version"}; !slices.Equal(keys, want) {
			t.Errorf("keys = %v, want %v", keys, want)
		}

		if got["allure_version"] != allureVersion {
			t.Errorf("allure_version = %v, want %v", got["allure_version"], allureVersion)
		}
		if got["service_version"] != serviceVersion {
			t.Errorf("service_version = %v, want %v", got["service_version"], serviceVersion)
		}
	})

	t.Run("empty versions are published, not omitted", func(t *testing.T) {

		s := NewServer("unused-dir", nil, RuntimeConfig{}, Versions{})

		w := httptest.NewRecorder()
		s.getVersion(w, httptest.NewRequest(http.MethodGet, "/version", nil))

		if got := strings.TrimSpace(w.Body.String()); got != `{"allure_version":"","service_version":""}` {
			t.Errorf("body = %s, want both keys with empty values", got)
		}
	})

	t.Run("the route is registered", func(t *testing.T) {

		s := NewServer("unused-dir", nil, RuntimeConfig{}, versions)

		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/version", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", w.Code, http.StatusOK, w.Body.String())
		}
		want := `{"allure_version":"3.14.3","service_version":"0.0.2-test"}`
		if got := strings.TrimSpace(w.Body.String()); got != want {
			t.Errorf("body = %s, want %s", got, want)
		}
	})
}
