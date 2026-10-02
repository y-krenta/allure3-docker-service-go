package config

import (
	"cmp"
	"log"
	"os"
	"strconv"
	"time"
)

// Config holds runtime configuration loaded from environment variables.
// Use Load to obtain a populated instance; the zero value is not meaningful.
type Config struct {
	Port                 string        // listen port
	SecurityEnable       bool          // Enable JWT auth; refused at startup, not implemented
	KeepHistory          bool          // Preserve Allure history across runs
	KeepHistoryLatest    int           // How many history entries to retain
	CheckResultsInterval time.Duration // Auto-generate interval
	OptimizeStorage      bool          // Strip large attachments; parsed, not implemented yet
	TLS                  bool          // Enable HTTPS; refused at startup, not implemented
	DevMode              bool          // Debug reloader; parsed, not implemented yet
	ProjectsDir          string        // Default path projects
	AllureBin            string        // Allure CLI executable; a bare name is looked up in PATH
	PublicBaseURL        string        // Public address of this service; required, validated in main
	MaxConcurrentBuilds  int           // Builds running at once across all projects; 0 means 1, negative falls back to the default
	BuildHeapMB          int           // V8 old-space cap of one build in MiB; 0 leaves it to Node, 1-255 falls back to the default
}

const (
	// defaultBuildHeapMB is enough for ~10 000 tests with 60 runs of history.
	defaultBuildHeapMB = 2048
	// minBuildHeapMB is the smallest BUILD_HEAP_MB taken at its word. Below
	// it every build would fail with "JavaScript heap out of memory", so a
	// smaller value is read as a mistake - most likely the unit taken for GB.
	minBuildHeapMB = 256
)

// Load reads configuration from environment variables, applying defaults
// for any that are unset (see the Config field comments for the env var
// names and defaults). It never fails; invalid values fall back to defaults
// with a logged warning.
func Load() Config {
	var config Config
	config.Port = cmp.Or(os.Getenv("PORT"), "5050")
	config.SecurityEnable = getEnvAsBool("SECURITY_ENABLED", false)
	config.KeepHistory = getEnvAsBool("KEEP_HISTORY", true)
	config.KeepHistoryLatest = getEnvAsInt("KEEP_HISTORY_LATEST", 60)
	config.CheckResultsInterval = getEnvAsDurationSeconds("CHECK_RESULTS_EVERY_SECONDS", 0)
	config.OptimizeStorage = getEnvAsBool("OPTIMIZE_STORAGE", false)
	config.TLS = getEnvAsBool("TLS", false)
	config.DevMode = getEnvAsBool("DEV_MODE", false)
	config.ProjectsDir = cmp.Or(os.Getenv("STATIC_CONTENT_PROJECTS"), "/app/projects")
	config.AllureBin = cmp.Or(os.Getenv("ALLURE_BIN"), "allure")
	config.PublicBaseURL = os.Getenv("PUBLIC_BASE_URL")
	config.MaxConcurrentBuilds = max(getEnvAsInt("MAX_CONCURRENT_BUILDS", 4), 1)
	config.BuildHeapMB = getEnvAsInt("BUILD_HEAP_MB", defaultBuildHeapMB)
	if config.BuildHeapMB > 0 && config.BuildHeapMB < minBuildHeapMB {
		log.Printf("[WARN] BUILD_HEAP_MB=%d, below %d, using %d", config.BuildHeapMB, minBuildHeapMB,
			defaultBuildHeapMB)
		config.BuildHeapMB = defaultBuildHeapMB
	}

	return config

}

// getEnvAsBool parses the env var key as a bool, returning defaultValue if
// it is unset or fails to parse.
func getEnvAsBool(key string, defaultValue bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return defaultValue
	}
	val, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("[WARN] %s=%q not a bool (%v), using %v", key, raw, err, defaultValue)
		return defaultValue
	}
	return val
}

// getEnvAsInt parses the env var key as a non-negative int, returning
// defaultValue if it is unset, fails to parse, or is negative.
func getEnvAsInt(key string, defaultValue int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return defaultValue
	}

	val, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("[WARN] %s=%q malformed, using %d", key, raw, defaultValue)
		return defaultValue
	}

	if val < 0 {
		log.Printf("[WARN] %s=%d negative, using %d", key, val, defaultValue)
		return defaultValue
	}

	return val
}

// getEnvAsDurationSeconds parses the env var key as a whole number of
// seconds and returns it as a time.Duration, returning defaultValue if it
// is unset, fails to parse, or is not positive.
func getEnvAsDurationSeconds(key string, defaultValue time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}

	sec, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("[WARN] %s=%q not a number, using %v", key, v, defaultValue)
		return defaultValue
	}

	if sec <= 0 {
		log.Printf("[WARN] %s=%d <= 0, using %v", key, sec, defaultValue)
		return defaultValue
	}

	return time.Duration(sec) * time.Second
}
