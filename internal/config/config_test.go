package config

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allKeys lists every variable Load reads, so a test can clear the ones the
// developer's shell happens to set.
var allKeys = []string{
	"PORT", "SECURITY_ENABLED", "KEEP_HISTORY", "KEEP_HISTORY_LATEST",
	"CHECK_RESULTS_EVERY_SECONDS", "OPTIMIZE_STORAGE", "TLS", "DEV_MODE",
	"STATIC_CONTENT_PROJECTS", "ALLURE_BIN", "PUBLIC_BASE_URL",
	"MAX_CONCURRENT_BUILDS", "BUILD_HEAP_MB",
	"SECURITY_USER", "SECURITY_PASS", "SECURITY_VIEWER_USER", "SECURITY_VIEWER_PASS",
	"MAKE_VIEWER_ENDPOINTS_PUBLIC", "JWT_SECRET_KEY", "ACCESS_TOKEN_TTL", "REFRESH_TOKEN_TTL",
}

// clearEnv unsets every variable Load reads for the rest of the test.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range allKeys {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	got, err := Load()
	require.NoError(t, err)

	assert.Equal(t, Config{
		Port:                      "5050",
		SecurityEnable:            false,
		KeepHistory:               true,
		KeepHistoryLatest:         60,
		CheckResultsEverySeconds:  0,
		OptimizeStorage:           false,
		TLS:                       false,
		DevMode:                   false,
		ProjectsDir:               "/app/projects",
		AllureBin:                 "allure",
		PublicBaseURL:             "",
		MaxConcurrentBuilds:       4,
		BuildHeapMB:               2048,
		SecurityUser:              "",
		SecurityPass:              "",
		SecurityViewerUser:        "",
		SecurityViewerPass:        "",
		MakeViewerEndpointsPublic: false,
		JWTSecretKey:              "",
		AccessTokenTTL:            15 * time.Minute,
		RefreshTokenTTL:           720 * time.Hour,
	}, got)
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("SECURITY_ENABLED", "true")
	t.Setenv("KEEP_HISTORY", "false")
	t.Setenv("KEEP_HISTORY_LATEST", "5")
	t.Setenv("CHECK_RESULTS_EVERY_SECONDS", "30")
	t.Setenv("OPTIMIZE_STORAGE", "true")
	t.Setenv("TLS", "true")
	t.Setenv("DEV_MODE", "true")
	t.Setenv("STATIC_CONTENT_PROJECTS", "/data/projects")
	t.Setenv("ALLURE_BIN", "/opt/allure/bin/allure")
	t.Setenv("PUBLIC_BASE_URL", "https://allure.example.com")
	t.Setenv("MAX_CONCURRENT_BUILDS", "2")
	t.Setenv("BUILD_HEAP_MB", "3072")
	t.Setenv("SECURITY_USER", "admin")
	t.Setenv("SECURITY_PASS", "admin-pass")
	t.Setenv("SECURITY_VIEWER_USER", "viewer")
	t.Setenv("SECURITY_VIEWER_PASS", "viewer-pass")
	t.Setenv("MAKE_VIEWER_ENDPOINTS_PUBLIC", "true")
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("ACCESS_TOKEN_TTL", "5m")
	t.Setenv("REFRESH_TOKEN_TTL", "24h")

	got, err := Load()
	require.NoError(t, err)

	assert.Equal(t, Config{
		Port:                      "8080",
		SecurityEnable:            true,
		KeepHistory:               false,
		KeepHistoryLatest:         5,
		CheckResultsEverySeconds:  30,
		OptimizeStorage:           true,
		TLS:                       true,
		DevMode:                   true,
		ProjectsDir:               "/data/projects",
		AllureBin:                 "/opt/allure/bin/allure",
		PublicBaseURL:             "https://allure.example.com",
		MaxConcurrentBuilds:       2,
		BuildHeapMB:               3072,
		SecurityUser:              "admin",
		SecurityPass:              "admin-pass",
		SecurityViewerUser:        "viewer",
		SecurityViewerPass:        "viewer-pass",
		MakeViewerEndpointsPublic: true,
		JWTSecretKey:              "0123456789abcdef0123456789abcdef",
		AccessTokenTTL:            5 * time.Minute,
		RefreshTokenTTL:           24 * time.Hour,
	}, got)
}

// TestLoadRejectsBadValues checks that a value Load cannot use fails it, with
// an error that names the variable and quotes the value as KEY="value", so an
// operator sees what to fix without reading the code.
func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"SECURITY_ENABLED", "yes please"},
		{"KEEP_HISTORY", "maybe"},
		{"KEEP_HISTORY_LATEST", "many"},
		{"KEEP_HISTORY_LATEST", "-1"},
		{"CHECK_RESULTS_EVERY_SECONDS", "30s"},
		{"CHECK_RESULTS_EVERY_SECONDS", "-5"},
		{"MAX_CONCURRENT_BUILDS", "0"},
		{"MAX_CONCURRENT_BUILDS", "-1"},
		{"BUILD_HEAP_MB", "1"},
		{"BUILD_HEAP_MB", "255"},
		{"BUILD_HEAP_MB", "-1"},
		{"MAKE_VIEWER_ENDPOINTS_PUBLIC", "maybe"},
		{"ACCESS_TOKEN_TTL", "15"},
		{"REFRESH_TOKEN_TTL", "a month"},
		{"ACCESS_TOKEN_TTL", "0s"},
		{"ACCESS_TOKEN_TTL", "-5m"},
		{"REFRESH_TOKEN_TTL", "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := Load()

			require.Error(t, err)
			assert.ErrorContains(t, err, tt.key+"="+strconv.Quote(tt.value))
		})
	}
}

// TestLoadReportsEveryBadValue checks that one failed Load names every bad
// variable, so an operator fixes them all in one restart.
func TestLoadReportsEveryBadValue(t *testing.T) {
	t.Run("unparsable values", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("KEEP_HISTORY", "maybe")
		t.Setenv("KEEP_HISTORY_LATEST", "many")

		_, err := Load()

		require.Error(t, err)
		assert.ErrorContains(t, err, `KEEP_HISTORY="maybe"`)
		assert.ErrorContains(t, err, `KEEP_HISTORY_LATEST="many"`)
	})

	t.Run("out-of-range values", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("MAX_CONCURRENT_BUILDS", "0")
		t.Setenv("BUILD_HEAP_MB", "100")

		_, err := Load()

		require.Error(t, err)
		assert.ErrorContains(t, err, `MAX_CONCURRENT_BUILDS="0"`)
		assert.ErrorContains(t, err, `BUILD_HEAP_MB="100"`)
	})

	t.Run("missing security settings", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("SECURITY_ENABLED", "true")

		_, err := Load()

		require.Error(t, err)
		assert.ErrorContains(t, err, "SECURITY_USER")
		assert.ErrorContains(t, err, "SECURITY_PASS")
		assert.ErrorContains(t, err, "JWT_SECRET_KEY")
	})
}

// TestLoadAcceptsEdgeValues checks the smallest values that still mean
// something: no history kept, the watcher off, a single build slot, the heap
// left to Node, the smallest heap taken at its word, and the shortest token
// lifetimes.
func TestLoadAcceptsEdgeValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"KEEP_HISTORY_LATEST", "0"},
		{"CHECK_RESULTS_EVERY_SECONDS", "0"},
		{"MAX_CONCURRENT_BUILDS", "1"},
		{"BUILD_HEAP_MB", "0"},
		{"BUILD_HEAP_MB", "256"},
		{"ACCESS_TOKEN_TTL", "1ns"},
		{"REFRESH_TOKEN_TTL", "1ns"},
	}

	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := Load()

			assert.NoError(t, err)
		})
	}
}

// setSecurity sets a complete, valid security configuration: auth on, an
// admin, a viewer and a signing key of exactly the minimum length.
func setSecurity(t *testing.T) {
	t.Helper()

	clearEnv(t)
	t.Setenv("SECURITY_ENABLED", "true")
	t.Setenv("SECURITY_USER", "admin")
	t.Setenv("SECURITY_PASS", "admin-secret-pass")
	t.Setenv("SECURITY_VIEWER_USER", "viewer")
	t.Setenv("SECURITY_VIEWER_PASS", "viewer-secret-pass")
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef")
}

// TestLoadRejectsIncompleteSecurity checks that with SECURITY_ENABLED on, Load
// fails on a setting auth cannot run safely without, naming the variable but
// never echoing a password or the signing key: the error goes to the log.
func TestLoadRejectsIncompleteSecurity(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		key  string
	}{
		{"no admin user", map[string]string{"SECURITY_USER": ""}, "SECURITY_USER"},
		{"no admin password", map[string]string{"SECURITY_PASS": ""}, "SECURITY_PASS"},
		{"no signing key", map[string]string{"JWT_SECRET_KEY": ""}, "JWT_SECRET_KEY"},
		{"signing key one byte short", map[string]string{"JWT_SECRET_KEY": "0123456789abcdef0123456789abcde"}, "JWT_SECRET_KEY"},
		{"viewer without a password", map[string]string{"SECURITY_VIEWER_PASS": ""}, "SECURITY_VIEWER_PASS"},
		{"viewer password without a user", map[string]string{"SECURITY_VIEWER_USER": ""}, "SECURITY_VIEWER_USER"},
		{"viewer named as the admin", map[string]string{"SECURITY_VIEWER_USER": "admin"}, "SECURITY_VIEWER_USER"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setSecurity(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			_, err := Load()

			require.Error(t, err)
			assert.ErrorContains(t, err, tt.key)
			for _, secret := range []string{"admin-secret-pass", "viewer-secret-pass", "0123456789abcdef"} {
				assert.NotContains(t, err.Error(), secret)
			}
		})
	}
}

// TestLoadAcceptsPartialSecurity checks the settings that are complete without
// every auth variable: an admin with no viewer, and auth off with a half-set
// viewer that nothing reads.
func TestLoadAcceptsPartialSecurity(t *testing.T) {
	t.Run("admin without a viewer", func(t *testing.T) {
		setSecurity(t)
		t.Setenv("SECURITY_VIEWER_USER", "")
		t.Setenv("SECURITY_VIEWER_PASS", "")

		_, err := Load()

		assert.NoError(t, err)
	})

	t.Run("auth off with a half-set viewer", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("SECURITY_VIEWER_USER", "viewer")

		_, err := Load()

		assert.NoError(t, err)
	})
}
