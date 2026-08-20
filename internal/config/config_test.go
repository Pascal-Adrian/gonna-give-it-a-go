package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envKeys are cleared before every case so real credentials cannot leak in.
var envKeys = []string{"ASANA_TOKEN", "ASANA_WORKSPACE_ID", "ASANA_OUT_DIR"}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		dotenv  string
		want    Config
		wantErr []string // substrings the error must mention
	}{
		{
			name: "values from environment",
			env:  map[string]string{"ASANA_TOKEN": "tok", "ASANA_WORKSPACE_ID": "123"},
			want: Config{Token: "tok", WorkspaceGID: "123", OutDir: "out"},
		},
		{
			name: "out dir override",
			env:  map[string]string{"ASANA_TOKEN": "tok", "ASANA_WORKSPACE_ID": "123", "ASANA_OUT_DIR": "export"},
			want: Config{Token: "tok", WorkspaceGID: "123", OutDir: "export"},
		},
		{
			name: "whitespace out dir falls back to the default",
			env:  map[string]string{"ASANA_TOKEN": "tok", "ASANA_WORKSPACE_ID": "123", "ASANA_OUT_DIR": "   "},
			want: Config{Token: "tok", WorkspaceGID: "123", OutDir: "out"},
		},
		{
			name:   "values from .env",
			dotenv: "ASANA_TOKEN=filetok\nASANA_WORKSPACE_ID=456\n",
			want:   Config{Token: "filetok", WorkspaceGID: "456", OutDir: "out"},
		},
		{
			name:   "environment beats .env",
			env:    map[string]string{"ASANA_TOKEN": "envtok", "ASANA_WORKSPACE_ID": "789"},
			dotenv: "ASANA_TOKEN=filetok\nASANA_WORKSPACE_ID=456\n",
			want:   Config{Token: "envtok", WorkspaceGID: "789", OutDir: "out"},
		},
		{
			// As produced by an unresolved CI secret or "docker run -e VAR".
			name:   "empty environment does not shadow .env",
			env:    map[string]string{"ASANA_TOKEN": "", "ASANA_WORKSPACE_ID": ""},
			dotenv: "ASANA_TOKEN=filetok\nASANA_WORKSPACE_ID=456\n",
			want:   Config{Token: "filetok", WorkspaceGID: "456", OutDir: "out"},
		},
		{
			name:    "both missing reported together",
			wantErr: []string{"ASANA_TOKEN", "ASANA_WORKSPACE_ID"},
		},
		{
			name:    "whitespace is not a token",
			env:     map[string]string{"ASANA_TOKEN": "   ", "ASANA_WORKSPACE_ID": "123"},
			wantErr: []string{"ASANA_TOKEN"},
		},
		{
			name:    "workspace name rejected in place of a gid",
			env:     map[string]string{"ASANA_TOKEN": "tok", "ASANA_WORKSPACE_ID": "Acme Inc"},
			wantErr: []string{"ASANA_WORKSPACE_ID", "not a gid"},
		},
		{
			name:    "malformed .env is reported",
			dotenv:  "this line has no separator\n",
			wantErr: []string{"reading .env"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			for _, key := range envKeys {
				unset(t, key)
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if tt.dotenv != "" {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tt.dotenv), 0o600); err != nil {
					t.Fatalf("writing .env: %v", err)
				}
			}

			got, err := Load()

			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("Load() = %+v, want error", got)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				if got != (Config{}) {
					t.Errorf("Load() = %+v on error, want zero Config", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadLeavesEnvironmentAlone: a token in os.Environ() would be inherited
// by every child process.
func TestLoadLeavesEnvironmentAlone(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, key := range envKeys {
		unset(t, key)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ASANA_TOKEN=secret\nASANA_WORKSPACE_ID=123\n"), 0o600); err != nil {
		t.Fatalf("writing .env: %v", err)
	}

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got, ok := os.LookupEnv("ASANA_TOKEN"); ok {
		t.Errorf("ASANA_TOKEN leaked into the environment as %q", got)
	}
}

// unset removes a variable for the test, via t.Setenv so it is restored after.
func unset(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetting %s: %v", key, err)
	}
}
