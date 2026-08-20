package extract

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pascal-Adrian/gonna-give-it-a-go/internal/asana"
)

// Both fakes are driven from the two polling goroutines at once.
type fakeSource struct {
	mu            sync.Mutex
	users         []asana.User
	usersErr      error
	projects      []asana.Project
	projectsErr   error
	usersCalls    int
	projectsCalls int
}

func (f *fakeSource) Users(context.Context) ([]asana.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usersCalls++
	return f.users, f.usersErr
}

func (f *fakeSource) Projects(context.Context) ([]asana.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projectsCalls++
	return f.projects, f.projectsErr
}

func (f *fakeSource) calls() (users, projects int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usersCalls, f.projectsCalls
}

type fakeStore struct {
	mu        sync.Mutex
	saved     map[string][]string // category to ids
	failOn    string
	unchanged map[string]bool // ids Save reports as already current
}

func (s *fakeStore) Save(category, id string, _ any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.failOn {
		return false, errors.New("disk on fire")
	}
	if s.saved == nil {
		s.saved = map[string][]string{}
	}
	s.saved[category] = append(s.saved[category], id)
	return !s.unchanged[id], nil
}

func (s *fakeStore) ids(category string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.saved[category])
}

func newService(source Source, store Store) *Service {
	return New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

var (
	twoUsers    = []asana.User{{GID: "1"}, {GID: "2"}}
	twoProjects = []asana.Project{{GID: "10"}, {GID: "11"}}
)

func TestCategories(t *testing.T) {
	tests := []struct {
		name    string
		source  *fakeSource
		failOn  string
		run     func(*Service, context.Context) error
		want    []string
		inside  string
		wantErr []string
	}{
		{
			name:   "users are saved",
			source: &fakeSource{users: twoUsers},
			run:    func(s *Service, ctx context.Context) error { return s.Users(ctx) },
			inside: "users",
			want:   []string{"1", "2"},
		},
		{
			name:   "projects are saved",
			source: &fakeSource{projects: twoProjects},
			run:    func(s *Service, ctx context.Context) error { return s.Projects(ctx) },
			inside: "projects",
			want:   []string{"10", "11"},
		},
		{
			name:    "a fetch failure is reported",
			source:  &fakeSource{usersErr: errors.New("boom")},
			run:     func(s *Service, ctx context.Context) error { return s.Users(ctx) },
			inside:  "users",
			want:    nil,
			wantErr: []string{"fetching users", "boom"},
		},
		{
			name:    "a save failure ends the category",
			source:  &fakeSource{users: twoUsers},
			failOn:  "1",
			run:     func(s *Service, ctx context.Context) error { return s.Users(ctx) },
			inside:  "users",
			want:    nil,
			wantErr: []string{"saving users", "disk on fire"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{failOn: tt.failOn}

			err := tt.run(newService(tt.source, store), t.Context())

			if len(tt.wantErr) == 0 && err != nil {
				t.Fatalf("run error = %v", err)
			}
			for _, want := range tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("error %v does not mention %q", err, want)
				}
			}
			if got := store.ids(tt.inside); !slices.Equal(got, tt.want) {
				t.Errorf("%s = %v, want %v", tt.inside, got, tt.want)
			}
		})
	}
}

// A category that fails must not prevent the other from being written, which
// is now a property of them running independently rather than of any code
// that coordinates them.
func TestCategoriesAreIndependent(t *testing.T) {
	source := &fakeSource{usersErr: errors.New("boom"), projects: twoProjects}
	store := &fakeStore{}
	svc := newService(source, store)

	if err := svc.Users(t.Context()); err == nil {
		t.Fatal("Users() error = nil, want the fetch error")
	}
	if err := svc.Projects(t.Context()); err != nil {
		t.Fatalf("Projects() error = %v", err)
	}

	if got := store.ids("projects"); !slices.Equal(got, []string{"10", "11"}) {
		t.Errorf("projects = %v, want both saved", got)
	}
}

func TestPollRunsBothCategoriesUntilCancelled(t *testing.T) {
	source := &fakeSource{users: twoUsers, projects: twoProjects}
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- newService(source, &fakeStore{}).Poll(ctx, 40*time.Millisecond, 10*time.Millisecond)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Poll() did not return after cancellation")
	}

	users, projects := source.calls()
	if users < 2 {
		t.Errorf("users polled %d times, want at least 2", users)
	}
	// Projects tick four times as often, so they must clearly outpace users.
	if projects <= users {
		t.Errorf("projects polled %d times and users %d, want projects to run more often", projects, users)
	}
}

// A failing category must keep ticking rather than ending the process.
func TestPollKeepsGoingAfterFailure(t *testing.T) {
	source := &fakeSource{usersErr: errors.New("boom"), projectsErr: errors.New("boom")}
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- newService(source, &fakeStore{}).Poll(ctx, 10*time.Millisecond, 10*time.Millisecond)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if users, _ := source.calls(); users < 2 {
		t.Errorf("users polled %d times after failing, want it to keep retrying", users)
	}
}

func TestPollReturnsPromptlyWhenAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- newService(&fakeSource{}, &fakeStore{}).Poll(ctx, time.Hour, time.Hour)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Poll() blocked on a cancelled context")
	}
}
