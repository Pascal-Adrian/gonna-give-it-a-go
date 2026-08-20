// Package extract fetches Asana objects and saves them, one category at a
// time.
package extract

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/asana"
)

// Source is the part of the Asana client this package needs.
type Source interface {
	Users(ctx context.Context) ([]asana.User, error)
	Projects(ctx context.Context) ([]asana.Project, error)
}

// Store persists one object under a category.
type Store interface {
	Save(category, id string, v any) error
}

type Service struct {
	source Source
	store  Store
	log    *slog.Logger
}

func New(source Source, store Store, log *slog.Logger) *Service {
	return &Service{source: source, store: store, log: log}
}

// Run extracts every category. One category failing does not stop the next,
// so a failed projects fetch still leaves the users on disk.
func (s *Service) Run(ctx context.Context) error {
	var errs []error

	if err := extractCategory(ctx, s, "users", s.source.Users, func(u asana.User) string { return u.GID }); err != nil {
		errs = append(errs, err)
	}
	// An interrupted run should stop rather than start the next category.
	if err := ctx.Err(); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := extractCategory(ctx, s, "projects", s.source.Projects, func(p asana.Project) string { return p.GID }); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// extractCategory fetches one category and saves every object in it. A save failure
// ends the category: the causes are systemic (permissions, a full disk), so
// carrying on would only repeat the same error per object.
func extractCategory[T any](ctx context.Context, s *Service, category string, fetch func(context.Context) ([]T, error), id func(T) string) error {
	items, err := fetch(ctx)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", category, err)
	}

	for _, item := range items {
		if err := s.store.Save(category, id(item), item); err != nil {
			return fmt.Errorf("saving %s: %w", category, err)
		}
	}

	s.log.Info("saved category", "category", category, "count", len(items))
	return nil
}
