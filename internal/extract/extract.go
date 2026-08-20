// Package extract mirrors an Asana workspace into a directory, polling each
// category on its own interval. Objects deleted in Asana keep their files:
// nothing here removes what it did not just fetch.
package extract

import (
	"context"
	"errors"
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

// Poll keeps both categories current until ctx ends, or until one of them
// fails in a way that retrying cannot fix. Each runs on its own schedule,
// which is also what isolates them: neither can delay or fail the other.
func (s *Service) Poll(ctx context.Context, usersEvery, projectsEvery time.Duration) error {
	if usersEvery <= 0 || projectsEvery <= 0 {
		return fmt.Errorf("poll intervals must be positive, got users %v and projects %v", usersEvery, projectsEvery)
	}

	// A credential that stops working stops both categories, so the first
	// fatal error ends the run rather than being retried until the process is
	// killed.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	categories := []struct {
		every time.Duration
		run   func(context.Context) error
	}{
		{usersEvery, s.Users},
		{projectsEvery, s.Projects},
	}

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	for _, c := range categories {
		wg.Go(func() {
			if err := s.repeat(ctx, c.every, c.run); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				cancel()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// repeat runs now, then once per interval, until ctx ends. A failure is logged
// and left for the next run: a transient error should not end a process meant
// to stay up. One that retrying cannot fix is returned instead.
//
// The wait starts when a run finishes rather than when it began, so a run that
// overtakes its own interval cannot queue the next one back to back.
func (s *Service) repeat(ctx context.Context, every time.Duration, run func(context.Context) error) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		started := time.Now()
		if err := run(ctx); err != nil && ctx.Err() == nil {
			if asana.Fatal(err) {
				return err
			}
			s.log.Error("poll failed", "err", err, "retry_in", every)
		}
		if took := time.Since(started); took > every {
			s.log.Warn("poll is falling behind its interval", "took", took, "interval", every)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
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

	// Every poll is logged, including the ones that changed nothing: a silent
	// idle poller is indistinguishable from a wedged one. Set LOG_LEVEL above
	// info to quieten it.
	s.log.Info("saved category", "category", category, "changed", changed, "unchanged", len(items)-changed)
	return nil
}
