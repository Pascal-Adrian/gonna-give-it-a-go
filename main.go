// Command gonna-give-it-a-go mirrors the users and projects of an Asana
// workspace into a directory, one JSON file per object, re-reading each
// category on its own interval until interrupted.
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
	if err := run(); err != nil {
		// Printed, not logged: this runs before the logger exists, and slog
		// would escape the newlines between joined errors anyway.
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
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
