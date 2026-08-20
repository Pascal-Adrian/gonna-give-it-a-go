// Package config loads the extractor's settings from the environment, falling
// back to a .env file in the working directory.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	defaultOutDir       = "out"
	defaultPollUsers    = 5 * time.Minute
	defaultPollProjects = 30 * time.Second
	defaultRateLimit    = 150 // Asana's free tier; paid plans allow 1500.

	// Floors and ceilings that catch a typo rather than express a policy: a
	// sub-second interval polls in a near-tight loop, and a limit this large
	// is a pasted gid, not a plan.
	minInterval  = time.Second
	maxRateLimit = 10_000
)

// Config holds everything the extractor needs to run.
type Config struct {
	Token        string // Asana personal access token.
	WorkspaceGID string // Workspace or organization to extract from.
	OutDir       string // Directory the JSON files are written to.

	PollUsers    time.Duration // How often to re-read the user list.
	PollProjects time.Duration // How often to re-read the project list.

	// RateLimit is the requests per minute the plan allows, not the rate we
	// issue; the client paces below it.
	RateLimit int

	// LogLevel gates the output. Polls that change nothing log at debug, so
	// debug is how you confirm a quiet poller is still working.
	LogLevel slog.Level
}

// Load reads the configuration, preferring the environment over .env and
// reporting every problem at once. A missing .env is not an error.
//
// .env is parsed rather than loaded into the process environment, so the token
// stays out of os.Environ(), and a variable that is set but empty falls
// through to .env instead of shadowing it.
func Load() (Config, error) {
	file, err := godotenv.Read()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("reading .env: %w", err)
	}

	lookup := func(key string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return strings.TrimSpace(file[key])
	}

	cfg := Config{
		Token:        lookup("ASANA_TOKEN"),
		WorkspaceGID: lookup("ASANA_WORKSPACE_ID"),
		OutDir:       lookup("ASANA_OUT_DIR"),
	}
	if cfg.OutDir == "" {
		cfg.OutDir = defaultOutDir
	}

	var errs []error

	if cfg.PollUsers, err = duration(lookup("ASANA_POLL_USERS"), defaultPollUsers); err != nil {
		errs = append(errs, fmt.Errorf("ASANA_POLL_USERS: %w", err))
	}
	if cfg.PollProjects, err = duration(lookup("ASANA_POLL_PROJECTS"), defaultPollProjects); err != nil {
		errs = append(errs, fmt.Errorf("ASANA_POLL_PROJECTS: %w", err))
	}
	if cfg.RateLimit, err = rateLimit(lookup("ASANA_RATE_LIMIT"), defaultRateLimit); err != nil {
		errs = append(errs, fmt.Errorf("ASANA_RATE_LIMIT: %w", err))
	}
	if cfg.LogLevel, err = logLevel(lookup("LOG_LEVEL")); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	if cfg.Token == "" {
		errs = append(errs, errors.New(`ASANA_TOKEN is not set: export it, or add it to a .env file in the current directory (create a token under Asana > My Settings > Apps > Personal access tokens)`))
	}
	switch {
	case cfg.WorkspaceGID == "":
		errs = append(errs, errors.New(`ASANA_WORKSPACE_ID is not set: export it, or add it to a .env file in the current directory (list workspaces at https://app.asana.com/api/1.0/workspaces)`))
	case !isGID(cfg.WorkspaceGID):
		// A pasted workspace name would otherwise fail much later, as an
		// opaque API error.
		errs = append(errs, fmt.Errorf("ASANA_WORKSPACE_ID %q is not a gid: expected digits only, for example 1201234567890123", cfg.WorkspaceGID))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// duration parses a Go duration such as "30s", falling back to fallback when
// unset. Anything under minInterval is rejected: polling that fast would spend
// the whole rate budget re-reading data that cannot have changed.
func duration(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration, for example 30s or 5m", value)
	}
	if d < minInterval {
		return 0, fmt.Errorf("%v is too short an interval, expected at least %v", d, minInterval)
	}
	return d, nil
}

// rateLimit parses the plan's allowance, falling back to fallback when unset.
func rateLimit(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", value)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%d is not usable, expected more than zero", n)
	}
	if n > maxRateLimit {
		return 0, fmt.Errorf("%d is implausible for a plan limit, expected at most %d", n, maxRateLimit)
	}
	return n, nil
}

// logLevel parses a slog level name, defaulting to info.
func logLevel(value string) (slog.Level, error) {
	if value == "" {
		return slog.LevelInfo, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		return 0, fmt.Errorf("%q is not a level, expected debug, info, warn or error", value)
	}
	return level, nil
}

// isGID reports whether s is a non-empty string of decimal digits, the shape
// of every Asana resource id.
func isGID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
