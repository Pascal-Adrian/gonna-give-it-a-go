// Package config loads the extractor's settings from the environment, falling
// back to a .env file in the working directory.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

const defaultOutDir = "out"

// Config holds everything the extractor needs to run.
type Config struct {
	Token        string // Asana personal access token.
	WorkspaceGID string // Workspace or organization to extract from.
	OutDir       string // Directory the JSON files are written to.
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

// isGID reports whether s is a string of decimal digits, the shape of every
// Asana resource id.
func isGID(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
