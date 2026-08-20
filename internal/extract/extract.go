// Package extract keeps a directory in step with an Asana workspace, polling
// each category on its own interval.
package extract

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/asana"
)

// Source is the part of the Asana client this package needs.
type Source interface {
	Users(ctx context.Context) ([]asana.User, error)
	Projects(ctx context.Context) ([]asana.Project, error)
}

// Store persists one object under a category, reporting whether the object had
// changed.
type Store interface {
	Save(category, id string, v any) (bool, error)
}

type Service struct {
	source Source
	store  Store
	log    *slog.Logger
}

func New(source Source, store Store, log *slog.Logger) *Service {
	return &Service{source: source, store: store, log: log}
}

// Users writes every user in the workspace.
func (s *Service) Users(ctx context.Context) error {
	return extractCategory(ctx, s, "users", s.source.Users, func(u asana.User) string { return u.GID })
}

// Projects writes every project in the workspace.
func (s *Service) Projects(ctx context.Context) error {
	return extractCategory(ctx, s, "projects", s.source.Projects, func(p asana.Project) string { return p.GID })
}

// Poll keeps both categories current until ctx ends. Each runs on its own
// schedule, which is also what isolates them: neither can delay or fail the
// other.
func (s *Service) Poll(ctx context.Context, usersEvery, projectsEvery time.Duration) error {
	categories := []struct {
		every time.Duration
		run   func(context.Context) error
	}{
		{usersEvery, s.Users},
		{projectsEvery, s.Projects},
	}

	var wg sync.WaitGroup
	for _, c := range categories {
		wg.Go(func() { s.repeat(ctx, c.every, c.run) })
	}
	wg.Wait()
	return nil
}

// repeat runs now, then on every tick, until ctx ends. A failure is logged and
// left for the next tick: a transient error should not end a process meant to
// stay up.
func (s *Service) repeat(ctx context.Context, every time.Duration, run func(context.Context) error) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		if err := run(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("poll failed", "err", err, "retry_in", every)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// extractCategory fetches one category and saves every object in it,
// overwriting only the ones that changed. A save failure ends the category:
// the causes are systemic (permissions, a full disk), so carrying on would
// only repeat the same error per object.
func extractCategory[T any](ctx context.Context, s *Service, category string, fetch func(context.Context) ([]T, error), id func(T) string) error {
	items, err := fetch(ctx)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", category, err)
	}

	var changed int
	for n, item := range items {
		written, err := s.store.Save(category, id(item), item)
		if err != nil {
			// Say how much landed before giving up, so the count is never
			// missing from the log when it matters most.
			s.log.Info("saved category", "category", category, "changed", changed, "unchanged", n-changed)
			return fmt.Errorf("saving %s: %w", category, err)
		}
		if written {
			changed++
		}
	}

	s.log.Info("saved category", "category", category, "changed", changed, "unchanged", len(items)-changed)
	return nil
}
