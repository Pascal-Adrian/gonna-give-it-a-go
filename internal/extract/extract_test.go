package extract

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
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

	usersCalled    chan struct{}
	projectsCalled chan struct{}
}

func (f *fakeSource) Users(context.Context) ([]asana.User, error) {
	f.mu.Lock()
	f.usersCalls++
	f.mu.Unlock()
	f.signal(f.usersCalled)
	return f.users, f.usersErr
}

func (f *fakeSource) Projects(context.Context) ([]asana.Project, error) {
	f.mu.Lock()
	f.projectsCalls++
	f.mu.Unlock()
	f.signal(f.projectsCalled)
	return f.projects, f.projectsErr
}

// signal reports a call without ever blocking the poller.
func (f *fakeSource) signal(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
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

// unchangedIDs marks ids Save should report as already current.
func (s *fakeStore) markUnchanged(ids ...string) *fakeStore {
	s.unchanged = map[string]bool{}
	for _, id := range ids {
		s.unchanged[id] = true
	}
	return s
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

// pollFast keeps the polling tests quick. They wait on progress rather than
// the clock, so the interval only sets how soon the next tick comes.
const pollFast = 10 * time.Millisecond

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

// waitFor blocks until ch fires n times, failing the test rather than hanging
// if the poller stalls. Waiting on progress rather than the clock keeps these
// tests off wall-clock timing.
func waitFor(t *testing.T, ch chan struct{}, n int) {
	t.Helper()
	for range n {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a poll")
		}
	}
}

// A category that keeps failing must not stop the other from being written.
// With the categories in separate goroutines this is a property of Poll, so it
// has to be tested through Poll.
func TestPollCategoriesAreIndependent(t *testing.T) {
	source := &fakeSource{
		usersErr:       errors.New("boom"),
		projects:       twoProjects,
		usersCalled:    make(chan struct{}, 1),
		projectsCalled: make(chan struct{}, 1),
	}
	store := &fakeStore{}
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- newService(source, store).Poll(ctx, pollFast, pollFast) }()

	waitFor(t, source.usersCalled, 2)
	waitFor(t, source.projectsCalled, 2)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Poll() error = %v", err)
	}

	if got := store.ids("projects"); len(got) == 0 {
		t.Error("projects were never saved while users kept failing")
	}
}

func TestPollRunsBothCategoriesUntilCancelled(t *testing.T) {
	source := &fakeSource{
		users:          twoUsers,
		projects:       twoProjects,
		usersCalled:    make(chan struct{}, 1),
		projectsCalled: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- newService(source, &fakeStore{}).Poll(ctx, pollFast, pollFast) }()

	// Both must poll repeatedly; the exact counts are the scheduler's business.
	waitFor(t, source.usersCalled, 2)
	waitFor(t, source.projectsCalled, 2)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll() did not return after cancellation")
	}
}

// A transient failure must not end a process meant to stay up.
func TestPollKeepsGoingAfterFailure(t *testing.T) {
	source := &fakeSource{
		usersErr:    errors.New("boom"),
		projectsErr: errors.New("boom"),
		usersCalled: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- newService(source, &fakeStore{}).Poll(ctx, pollFast, pollFast) }()

	waitFor(t, source.usersCalled, 3)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
}

// A credential that stops working cannot be retried into working, so it must
// end the run and reach the caller rather than log forever.
func TestPollStopsOnFatalError(t *testing.T) {
	source := &fakeSource{usersErr: &asana.APIError{Status: http.StatusUnauthorized}}

	done := make(chan error, 1)
	go func() { done <- newService(source, &fakeStore{}).Poll(t.Context(), pollFast, pollFast) }()

	select {
	case err := <-done:
		var apiErr *asana.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
			t.Fatalf("Poll() error = %v, want the 401 to surface", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll() kept retrying a permanently failing credential")
	}
}

func TestPollReturnsPromptlyWhenAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	source := &fakeSource{}
	done := make(chan error, 1)
	go func() { done <- newService(source, &fakeStore{}).Poll(ctx, time.Hour, time.Hour) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll() blocked on a cancelled context")
	}

	// A cancelled context must not buy one last sweep on the way out.
	if users, projects := source.calls(); users != 0 || projects != 0 {
		t.Errorf("polled %d users and %d projects after cancellation, want none", users, projects)
	}
}

func TestPollRejectsUnusableIntervals(t *testing.T) {
	err := newService(&fakeSource{}, &fakeStore{}).Poll(t.Context(), 0, pollFast)
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("Poll() error = %v, want a complaint about the interval", err)
	}
}

// The changed/unchanged split is what the whole increment is for, so it needs
// a case where something is genuinely unchanged.
func TestUnchangedObjectsAreCounted(t *testing.T) {
	store := (&fakeStore{}).markUnchanged("1")
	source := &fakeSource{users: twoUsers}

	if err := newService(source, store).Users(t.Context()); err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if got := store.ids("users"); !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("users = %v, want both attempted", got)
	}
}
