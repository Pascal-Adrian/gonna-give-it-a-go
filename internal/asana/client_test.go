package asana

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// recorder captures the requests a handler receives.
type recorder struct {
	mu   sync.Mutex
	seen []url.Values
}

func (rec *recorder) add(r *http.Request) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.seen = append(rec.seen, r.URL.Query())
}

func (rec *recorder) queries() []url.Values {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.seen)
}

// newTestClient drops the rate limiter, which would otherwise pace every test
// at one request per 400ms.
func newTestClient(srv *httptest.Server) *Client {
	c := New("test-token", "9001", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.baseURL = srv.URL
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	return c
}

func TestUsersRequestShape(t *testing.T) {
	var rec recorder
	var auth, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		auth, accept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		io.WriteString(w, `{"data":[{"gid":"1","name":"Ada","email":"ada@example.com"}]}`)
	}))
	defer srv.Close()

	users, err := newTestClient(srv).Users(t.Context())
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}

	if len(users) != 1 {
		t.Fatalf("Users() returned %d users, want 1", len(users))
	}
	if users[0].GID != "1" || users[0].Email != "ada@example.com" {
		t.Errorf("Users() = %+v", users)
	}
	if auth != "Bearer test-token" {
		t.Errorf("Authorization = %q", auth)
	}
	if accept != "application/json" {
		t.Errorf("Accept = %q", accept)
	}

	q := rec.queries()[0]
	for key, want := range map[string]string{"workspace": "9001", "limit": "100"} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := q.Get("opt_fields"); got != userFields {
		t.Errorf("opt_fields = %q, want %q", got, userFields)
	}
}

func TestPaginationFollowsOffset(t *testing.T) {
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		switch r.URL.Query().Get("offset") {
		case "":
			io.WriteString(w, `{"data":[{"gid":"1"}],"next_page":{"offset":"page2"}}`)
		case "page2":
			io.WriteString(w, `{"data":[{"gid":"2"}],"next_page":null}`)
		default:
			t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	}))
	defer srv.Close()

	users, err := newTestClient(srv).Users(t.Context())
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}

	if len(users) != 2 {
		t.Fatalf("Users() returned %d users, want 2", len(users))
	}
	if users[0].GID != "1" || users[1].GID != "2" {
		t.Errorf("Users() = %+v, want gids 1 and 2", users)
	}
	if n := len(rec.queries()); n != 2 {
		t.Fatalf("made %d requests, want 2", n)
	}
	if got := rec.queries()[1].Get("offset"); got != "page2" {
		t.Errorf("second request offset = %q, want page2", got)
	}
}

func TestProjectDecoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{
			"gid":"77","name":"Roadmap",
			"archived":false,"completed":false,
			"completed_at":null,"created_at":"2026-01-02T03:04:05.000Z",
			"due_on":"2026-06-30","notes":"ship it","public":true,
			"current_status":{"title":"On track","color":"green"},
			"owner":{"gid":"5","name":"Ada"},
			"members":[{"gid":"5","name":"Ada"},{"gid":"6","name":"Grace"}]
		}],"next_page":null}`)
	}))
	defer srv.Close()

	projects, err := newTestClient(srv).Projects(t.Context())
	if err != nil {
		t.Fatalf("Projects() error = %v", err)
	}

	if len(projects) != 1 {
		t.Fatalf("Projects() returned %d projects, want 1", len(projects))
	}
	p := projects[0]
	if p.GID != "77" || p.Name != "Roadmap" || p.DueOn != "2026-06-30" {
		t.Errorf("Projects()[0] = %+v", p)
	}
	if p.CompletedAt != nil {
		t.Errorf("CompletedAt = %v, want nil", p.CompletedAt)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !p.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", p.CreatedAt, want)
	}
	if p.CurrentStatus == nil || p.CurrentStatus.Title != "On track" {
		t.Errorf("CurrentStatus = %+v", p.CurrentStatus)
	}
	if p.Owner == nil || p.Owner.Name != "Ada" || len(p.Members) != 2 {
		t.Errorf("Owner = %+v, Members = %+v", p.Owner, p.Members)
	}
}

func TestRetries(t *testing.T) {
	baseBackoff = time.Millisecond
	t.Cleanup(func() { baseBackoff = time.Second })

	tests := []struct {
		name       string
		responses  []int // status per attempt
		retryAfter string
		wantCalls  int
		wantErr    bool
	}{
		{name: "429 then success", responses: []int{429, 200}, retryAfter: "0", wantCalls: 2},
		{name: "500 then success", responses: []int{500, 200}, wantCalls: 2},
		{name: "503 twice then success", responses: []int{503, 503, 200}, wantCalls: 3},
		{name: "500 exhausts attempts", responses: []int{500, 500, 500, 500}, wantCalls: 3, wantErr: true},
		{name: "401 is final", responses: []int{401, 200}, wantCalls: 1, wantErr: true},
		{name: "402 is final", responses: []int{402, 200}, wantCalls: 1, wantErr: true},
		{name: "404 is final", responses: []int{404, 200}, wantCalls: 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				status := tt.responses[min(calls, len(tt.responses)-1)]
				calls++
				mu.Unlock()

				if status == http.StatusTooManyRequests && tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					io.WriteString(w, `{"data":[{"gid":"1"}]}`)
					return
				}
				io.WriteString(w, `{"errors":[{"message":"boom"}]}`)
			}))
			defer srv.Close()

			_, err := newTestClient(srv).Users(t.Context())

			if tt.wantErr && err == nil {
				t.Fatal("Users() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Users() error = %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != tt.wantCalls {
				t.Errorf("made %d requests, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestAPIErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"errors":[{"message":"Not Authorized"}]}`)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).Users(t.Context())

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Users() error = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
	for _, want := range []string{"401", "Not Authorized", "ASANA_TOKEN"} {
		if !strings.Contains(apiErr.Error(), want) {
			t.Errorf("error %q does not mention %q", apiErr, want)
		}
	}
}

// A body that is malformed is the server's own bug and will not improve, but a
// body that simply stops early is a read that may succeed next time.
func TestDecodeFailures(t *testing.T) {
	baseBackoff = time.Millisecond
	t.Cleanup(func() { baseBackoff = time.Second })

	tests := []struct {
		name      string
		body      string
		wantCalls int
	}{
		{name: "invalid syntax is final", body: `{"data":[{"gid": tru}]}`, wantCalls: 1},
		{name: "wrong type is final", body: `{"data":"not an array"}`, wantCalls: 1},
		{name: "truncated body is retried", body: `{"data":[{"gid":`, wantCalls: maxAttempts},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			if _, err := newTestClient(srv).Users(t.Context()); err == nil {
				t.Fatal("Users() error = nil, want decode error")
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != tt.wantCalls {
				t.Errorf("made %d requests, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestPaginationStallIsDetected(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		io.WriteString(w, `{"data":[{"gid":"1"}],"next_page":{"offset":"stuck"}}`)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).Users(t.Context())
	if err == nil || !strings.Contains(err.Error(), "pagination stalled") {
		t.Fatalf("Users() error = %v, want pagination stalled", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("made %d requests, want 2", calls)
	}
}

func TestCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := newTestClient(srv).Users(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Users() error = %v, want context.Canceled", err)
	}
}

func TestRetryAfterHeader(t *testing.T) {
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"5", 5 * time.Second},
		{" 2 ", 2 * time.Second},
		{"0", 0},
		{"", defaultRetryAfter},
		{"soon", defaultRetryAfter},
		{"-3", defaultRetryAfter},
		{"99999", maxRetryAfter},
		{"10000000000", maxRetryAfter}, // would overflow int64 nanoseconds
	}

	for _, tt := range tests {
		if got := retryAfter(tt.header); got != tt.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tt.header, got, tt.want)
		}
	}
}

// TestRateLimitPacing checks the limiter the constructor installs, which the
// other tests replace.
func TestRateLimitPacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "" {
			io.WriteString(w, `{"data":[{"gid":"1"}],"next_page":{"offset":"page2"}}`)
			return
		}
		io.WriteString(w, `{"data":[{"gid":"2"}]}`)
	}))
	defer srv.Close()

	c := New("test-token", "9001", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.baseURL = srv.URL

	if want := rate.Every(time.Minute / requestsPerMinute); c.limiter.Limit() != want {
		t.Errorf("limit = %v, want %v", c.limiter.Limit(), want)
	}
	if got := c.limiter.Burst(); got != 1 {
		t.Errorf("burst = %d, want 1", got)
	}

	start := time.Now()
	if _, err := c.Users(t.Context()); err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	// One token is available immediately; the second page waits for the next.
	if elapsed, want := time.Since(start), time.Minute/requestsPerMinute; elapsed < want-50*time.Millisecond {
		t.Errorf("two requests took %v, want at least %v", elapsed, want)
	}
}

// TestConnectionIsReusedAcrossPages guards the drain after a successful
// decode: bytes left unread past the JSON value make net/http discard the
// connection instead of returning it to the idle pool. The padding matters --
// the decoder's own buffer already swallows a trailing newline.
func TestConnectionIsReusedAcrossPages(t *testing.T) {
	var mu sync.Mutex
	opened := 0
	padding := "\n" + strings.Repeat(" ", 1024)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("offset") {
		case "":
			io.WriteString(w, `{"data":[{"gid":"1"}],"next_page":{"offset":"p2"}}`+padding)
		case "p2":
			io.WriteString(w, `{"data":[{"gid":"2"}],"next_page":{"offset":"p3"}}`+padding)
		default:
			io.WriteString(w, `{"data":[{"gid":"3"}]}`+padding)
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			opened++
			mu.Unlock()
		}
	}
	srv.Start()
	defer srv.Close()

	users, err := newTestClient(srv).Users(t.Context())
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("Users() returned %d users, want 3", len(users))
	}

	mu.Lock()
	defer mu.Unlock()
	if opened != 1 {
		t.Errorf("opened %d connections for 3 pages, want 1", opened)
	}
}
