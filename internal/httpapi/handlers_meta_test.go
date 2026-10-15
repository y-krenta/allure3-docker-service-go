package httpapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())

		assert.ElementsMatch(t, wantKeys, slices.Collect(maps.Keys(got)))
		assert.Equal(t, true, got["keep_history"])
		assert.Equal(t, float64(60), got["keep_history_latest"])
		assert.Equal(t, float64(30), got["check_results_every_seconds"])
	})

	t.Run("zero values are published, not omitted", func(t *testing.T) {

		s := NewServer("unused-dir", nil, RuntimeConfig{}, Versions{})

		w := httptest.NewRecorder()
		s.getConfig(w, httptest.NewRequest(http.MethodGet, "/config", nil))

		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())

		for _, k := range wantKeys {
			assert.Contains(t, got, k, w.Body.String())
		}
	})

	t.Run("the route is registered", func(t *testing.T) {

		s := NewServer("unused-dir", nil, RuntimeConfig{KeepHistoryLatest: 7}, Versions{})

		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/config", nil))

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())
		assert.Equal(t, float64(7), got["keep_history_latest"])
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

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())

		assert.ElementsMatch(t, []string{"allure_version", "service_version"}, slices.Collect(maps.Keys(got)))
		assert.Equal(t, allureVersion, got["allure_version"])
		assert.Equal(t, serviceVersion, got["service_version"])
	})

	t.Run("empty versions are published, not omitted", func(t *testing.T) {

		s := NewServer("unused-dir", nil, RuntimeConfig{}, Versions{})

		w := httptest.NewRecorder()
		s.getVersion(w, httptest.NewRequest(http.MethodGet, "/version", nil))

		assert.JSONEq(t, `{"allure_version":"","service_version":""}`, w.Body.String())
	})

	t.Run("the route is registered", func(t *testing.T) {

		s := NewServer("unused-dir", nil, RuntimeConfig{}, versions)

		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/version", nil))

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.JSONEq(t, `{"allure_version":"3.14.3","service_version":"0.0.2-test"}`, w.Body.String())
	})
}
