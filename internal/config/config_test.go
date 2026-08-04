package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// t.Setenv with an empty value still counts as "set", so unset every var
	// this package reads by pointing them at the empty string, which Load
	// treats the same as absent.
	for _, key := range []string{
		"PORT", "SECURITY_ENABLED", "KEEP_HISTORY", "KEEP_HISTORY_LATEST",
		"CHECK_RESULTS_EVERY_SECONDS", "OPTIMIZE_STORAGE", "TLS", "DEV_MODE",
		"STATIC_CONTENT_PROJECTS",
	} {
		t.Setenv(key, "")
	}

	got := Load()

	want := Config{
		Port:                 "5050",
		SecurityEnable:       false,
		KeepHistory:          false,
		KeepHistoryLatest:    25,
		CheckResultsInterval: 0,
		OptimizeStorage:      false,
		TLS:                  false,
		DevMode:              false,
		ProjectsDir:          "/app/projects",
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("SECURITY_ENABLED", "true")
	t.Setenv("KEEP_HISTORY", "1")
	t.Setenv("KEEP_HISTORY_LATEST", "5")
	t.Setenv("CHECK_RESULTS_EVERY_SECONDS", "30")
	t.Setenv("OPTIMIZE_STORAGE", "TRUE")
	t.Setenv("TLS", "t")
	t.Setenv("DEV_MODE", "true")
	t.Setenv("STATIC_CONTENT_PROJECTS", "/data/projects")

	got := Load()

	want := Config{
		Port:                 "8080",
		SecurityEnable:       true,
		KeepHistory:          true,
		KeepHistoryLatest:    5,
		CheckResultsInterval: 30 * time.Second,
		OptimizeStorage:      true,
		TLS:                  true,
		DevMode:              true,
		ProjectsDir:          "/data/projects",
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadFallsBackOnGarbage(t *testing.T) {
	t.Setenv("SECURITY_ENABLED", "yes please")
	t.Setenv("KEEP_HISTORY_LATEST", "many")
	t.Setenv("CHECK_RESULTS_EVERY_SECONDS", "-1")

	got := Load()

	if got.SecurityEnable {
		t.Errorf("SecurityEnable = true, want the false default")
	}
	if got.KeepHistoryLatest != 25 {
		t.Errorf("KeepHistoryLatest = %d, want the 25 default", got.KeepHistoryLatest)
	}
	if got.CheckResultsInterval != 0 {
		t.Errorf("CheckResultsInterval = %v, want the 0 default", got.CheckResultsInterval)
	}
}

func TestGetEnvAsBool(t *testing.T) {
	tests := []struct {
		name  string
		value string
		def   bool
		want  bool
	}{
		{name: "unset keeps the default", value: "", def: true, want: true},
		{name: "true", value: "true", def: false, want: true},
		{name: "one", value: "1", def: false, want: true},
		{name: "upper case", value: "TRUE", def: false, want: true},
		{name: "false overrides a true default", value: "false", def: true, want: false},
		{name: "zero overrides a true default", value: "0", def: true, want: false},
		{name: "garbage keeps the default", value: "maybe", def: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_BOOL", tt.value)

			if got := getEnvAsBool("TEST_BOOL", tt.def); got != tt.want {
				t.Errorf("getEnvAsBool(%q, %v) = %v, want %v", tt.value, tt.def, got, tt.want)
			}
		})
	}
}

func TestGetEnvAsInt(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "unset keeps the default", value: "", want: 25},
		{name: "positive", value: "7", want: 7},
		{name: "zero is allowed", value: "0", want: 0},
		{name: "negative keeps the default", value: "-1", want: 25},
		{name: "not a number keeps the default", value: "many", want: 25},
		{name: "float keeps the default", value: "1.5", want: 25},
		{name: "surrounding spaces keep the default", value: " 7 ", want: 25},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_INT", tt.value)

			if got := getEnvAsInt("TEST_INT", 25); got != tt.want {
				t.Errorf("getEnvAsInt(%q, 25) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestGetEnvAsDurationSeconds(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "unset keeps the default", value: "", want: 0},
		{name: "seconds become a duration", value: "30", want: 30 * time.Second},
		{name: "one second", value: "1", want: time.Second},
		{name: "zero keeps the default", value: "0", want: 0},
		{name: "negative keeps the default", value: "-5", want: 0},
		{name: "not a number keeps the default", value: "30s", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_DURATION", tt.value)

			if got := getEnvAsDurationSeconds("TEST_DURATION", 0); got != tt.want {
				t.Errorf("getEnvAsDurationSeconds(%q, 0) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}

	// A zero default cannot tell "sec <= 0" from "sec < 0" — both yield zero.
	// A non-zero default makes the boundary observable.
	t.Run("zero falls back to a non-zero default", func(t *testing.T) {
		t.Setenv("TEST_DURATION", "0")

		if got := getEnvAsDurationSeconds("TEST_DURATION", time.Minute); got != time.Minute {
			t.Errorf("getEnvAsDurationSeconds(%q, 1m) = %v, want 1m", "0", got)
		}
	})
}
