// Command gonna-give-it-a-go extracts the users and projects of an Asana
// workspace and writes them to disk as one JSON file per object.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/config"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		// Printed, not logged: slog would escape the newlines between joined
		// errors into one unreadable line.
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log.Info("configuration loaded", "workspace", cfg.WorkspaceGID, "out_dir", cfg.OutDir)
	return nil
}
