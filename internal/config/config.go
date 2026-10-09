package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds runtime configuration loaded from environment variables. Each
// field's env tag names its variable and envDefault gives the value used when
// the variable is unset or empty. Use Load to obtain a populated instance; the
// zero value is not meaningful.
type Config struct {
	// Port is the port the HTTP server listens on.
	Port string `env:"PORT" envDefault:"5050"`

	// SecurityEnable turns on JWT auth; not implemented yet, so main refuses
	// to start with it set.
	SecurityEnable bool `env:"SECURITY_ENABLED"`
	// SecurityUser is the admin's login name; required with SecurityEnable.
	SecurityUser string `env:"SECURITY_USER"`
	// SecurityPass is the admin's password.
	SecurityPass string `env:"SECURITY_PASS"`
	// SecurityViewerUser is the read-only viewer's login name; empty means
	// there is no viewer.
	SecurityViewerUser string `env:"SECURITY_VIEWER_USER"`
	// SecurityViewerPass is the viewer's password.
	SecurityViewerPass string `env:"SECURITY_VIEWER_PASS"`
	// MakeViewerEndpointsPublic opens the read-only endpoints to anyone,
	// without logging in.
	MakeViewerEndpointsPublic bool `env:"MAKE_VIEWER_ENDPOINTS_PUBLIC"`
	// JWTSecretKey signs and verifies the tokens. A restart with another key
	// invalidates every token issued, logging everyone out.
	JWTSecretKey string `env:"JWT_SECRET_KEY"`
	// AccessTokenTTL is how long an access token lives.
	AccessTokenTTL time.Duration `env:"ACCESS_TOKEN_TTL" envDefault:"15m"`
	// RefreshTokenTTL is how long a refresh token lives, and so how long a
	// login lasts without entering the password again.
	RefreshTokenTTL time.Duration `env:"REFRESH_TOKEN_TTL" envDefault:"720h"`

	// KeepHistory carries Allure history from one build into the next.
	KeepHistory bool `env:"KEEP_HISTORY" envDefault:"true"`
	// KeepHistoryLatest is how many past runs the history keeps; 0 or more.
	KeepHistoryLatest int `env:"KEEP_HISTORY_LATEST" envDefault:"60"`
	// CheckResultsEverySeconds is how often the watcher looks for new results,
	// in whole seconds; 0 turns the watcher off. Seconds rather than a
	// time.Duration because the variable's format is part of the contract
	// with operators.
	CheckResultsEverySeconds int `env:"CHECK_RESULTS_EVERY_SECONDS" envDefault:"0"`
	// OptimizeStorage strips large attachments; parsed, not implemented yet.
	OptimizeStorage bool `env:"OPTIMIZE_STORAGE"`
	// TLS serves HTTPS; not implemented, so main refuses to start with it set.
	// TLS belongs on the reverse proxy.
	TLS bool `env:"TLS"`
	// DevMode enables a debug reloader; parsed, not implemented yet.
	DevMode bool `env:"DEV_MODE"`
	// ProjectsDir is the root holding every project's directory.
	ProjectsDir string `env:"STATIC_CONTENT_PROJECTS" envDefault:"/app/projects"`
	// AllureBin is the Allure CLI executable; a bare name is looked up in PATH.
	AllureBin string `env:"ALLURE_BIN" envDefault:"allure"`
	// PublicBaseURL is the address clients reach this service at. It has no
	// default; main requires it and checks it is absolute.
	PublicBaseURL string `env:"PUBLIC_BASE_URL"`
	// MaxConcurrentBuilds is how many builds run at once across all
	// projects; 1 or more.
	MaxConcurrentBuilds int `env:"MAX_CONCURRENT_BUILDS" envDefault:"4"`
	// BuildHeapMB caps the V8 old space of one build, in MiB. 0 leaves it to
	// Node; otherwise at least minBuildHeapMB. The 2048 default is enough for
	// about 10 000 tests with 60 runs of history.
	BuildHeapMB int `env:"BUILD_HEAP_MB" envDefault:"2048"`
}

const (
	// minBuildHeapMB is the smallest BUILD_HEAP_MB taken at its word. Below
	// it every build would fail with "JavaScript heap out of memory", so a
	// smaller value is read as a mistake - most likely the unit taken for GB.
	minBuildHeapMB = 256
)

// Load reads configuration from environment variables, applying the defaults
// in Config's tags to any that are unset.
//
// A value Load cannot use is an error, never a silent fallback to the
// default: a service that quietly runs on another number is worse than one
// that does not start. That covers a value that does not parse into its
// field's type and one outside the range the field allows. The error names
// every bad variable, one per line as KEY="value": want ..., so an operator
// can fix them all from the message alone. A value that fails to parse is
// reported without the range checks, which would only add noise about a
// field left at zero.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, describeParseErrors(err)
	}
	var errs []error
	if cfg.MaxConcurrentBuilds < 1 {
		errs = append(errs, invalidEnv("MAX_CONCURRENT_BUILDS", "1 or more"))
	}
	if cfg.KeepHistoryLatest < 0 {
		errs = append(errs, invalidEnv("KEEP_HISTORY_LATEST", "0 or more"))
	}
	if cfg.CheckResultsEverySeconds < 0 {
		errs = append(errs, invalidEnv("CHECK_RESULTS_EVERY_SECONDS", "0 or more"))
	}
	if cfg.BuildHeapMB != 0 && cfg.BuildHeapMB < minBuildHeapMB {
		errs = append(errs, invalidEnv(
			"BUILD_HEAP_MB",
			fmt.Sprintf("0 (left to Node) or at least %d", minBuildHeapMB),
		))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// describeParseErrors rewrites the errors env.ParseAs returns for values that
// do not parse into their field's type. The library names the Go field
// ("KeepHistoryLatest"); an operator knows only the variable, so each part is
// rebuilt through invalidEnv from the field's env tag. Errors of any other
// kind are passed through unchanged.
func describeParseErrors(err error) error {
	var agg env.AggregateError
	if !errors.As(err, &agg) {
		return err
	}
	var errs []error
	for _, e := range agg.Errors {
		var pe env.ParseError
		if !errors.As(e, &pe) {
			errs = append(errs, e)
			continue
		}
		f, _ := reflect.TypeFor[Config]().FieldByName(pe.Name)
		key := f.Tag.Get("env")
		errs = append(errs, invalidEnv(key, pe.Type.String()))
	}
	return errors.Join(errs...)
}

// invalidEnv reports that the environment variable key holds a value Load
// cannot use, quoting the value as set and saying what was expected.
func invalidEnv(key, want string) error {
	return fmt.Errorf("%s=%q: want %s", key, os.Getenv(key), want)
}
