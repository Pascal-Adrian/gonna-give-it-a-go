package extract

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/asana"
)

type fakeSource struct {
	users       []asana.User
	usersErr    error
	projects    []asana.Project
	projectsErr error
}

func (f fakeSource) Users(context.Context) ([]asana.User, error) { return f.users, f.usersErr }

func (f fakeSource) Projects(context.Context) ([]asana.Project, error) {
	return f.projects, f.projectsErr
}

type fakeStore struct {
	saved  map[string][]string // category to ids
	failOn string
}

func (s *fakeStore) Save(category, id string, _ any) error {
	if id == s.failOn {
		return errors.New("disk on fire")
	}
	if s.saved == nil {
		s.saved = map[string][]string{}
	}
	s.saved[category] = append(s.saved[category], id)
	return nil
}

func newService(source Source, store Store) *Service {
	return New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

var (
	twoUsers    = []asana.User{{GID: "1"}, {GID: "2"}}
	twoProjects = []asana.Project{{GID: "10"}, {GID: "11"}}
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		source  fakeSource
		failOn  string
		want    map[string][]string
		wantErr []string
	}{
		{
			name:   "saves every object in both categories",
			source: fakeSource{users: twoUsers, projects: twoProjects},
			want:   map[string][]string{"users": {"1", "2"}, "projects": {"10", "11"}},
		},
		{
			name:    "a failed users fetch still leaves the projects",
			source:  fakeSource{usersErr: errors.New("boom"), projects: twoProjects},
			want:    map[string][]string{"projects": {"10", "11"}},
			wantErr: []string{"fetching users", "boom"},
		},
		{
			name:    "a failed projects fetch still leaves the users",
			source:  fakeSource{users: twoUsers, projectsErr: errors.New("boom")},
			want:    map[string][]string{"users": {"1", "2"}},
			wantErr: []string{"fetching projects", "boom"},
		},
		{
			name:    "both failures are reported",
			source:  fakeSource{usersErr: errors.New("no users"), projectsErr: errors.New("no projects")},
			want:    map[string][]string{},
			wantErr: []string{"fetching users", "no users", "fetching projects", "no projects"},
		},
		{
			name:    "a save failure ends its category but not the run",
			source:  fakeSource{users: twoUsers, projects: twoProjects},
			failOn:  "1",
			want:    map[string][]string{"projects": {"10", "11"}},
			wantErr: []string{"saving users", "disk on fire"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{failOn: tt.failOn}

			err := newService(tt.source, store).Run(t.Context())

			if len(tt.wantErr) == 0 && err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, want := range tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("error %v does not mention %q", err, want)
				}
			}
			if got := len(store.saved); got != len(tt.want) {
				t.Fatalf("saved %d categories, want %d: %v", got, len(tt.want), store.saved)
			}
			for category, wantIDs := range tt.want {
				gotIDs := store.saved[category]
				if !slices.Equal(gotIDs, wantIDs) {
					t.Errorf("%s = %v, want %v", category, gotIDs, wantIDs)
				}
			}
		})
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	store := &fakeStore{}
	err := newService(fakeSource{users: twoUsers, projects: twoProjects}, store).Run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	// Asserting users landed is what pins the check between the categories:
	// a check at the top of Run would skip both and still pass the rest.
	if got := store.saved["users"]; !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("users = %v, want the category to have finished", got)
	}
	if _, ok := store.saved["projects"]; ok {
		t.Error("projects were extracted after cancellation")
	}
}
