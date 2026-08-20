// Command gonna-give-it-a-go extracts the users and projects of an Asana
// workspace and writes them to disk as one JSON file per object.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/asana"
	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/config"
	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/extract"
	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/store"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log.Info("configuration loaded",
		"workspace", cfg.WorkspaceGID,
		"out_dir", cfg.OutDir,
		"rate_limit", cfg.RateLimit,
		"poll_users", cfg.PollUsers,
		"poll_projects", cfg.PollProjects,
	)

	client := asana.New(cfg.Token, cfg.WorkspaceGID, cfg.RateLimit, log)
	svc := extract.New(client, store.New(cfg.OutDir), log)
	return svc.Poll(ctx, cfg.PollUsers, cfg.PollProjects)
}
