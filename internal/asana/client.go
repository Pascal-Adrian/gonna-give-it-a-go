// Package asana reads users and projects from the Asana REST API.
package asana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL = "https://app.asana.com/api/1.0"

	// Asana allows 150 requests per minute on the free tier, 1500 on paid
	// plans. A burst of 1 spreads requests evenly; the margin below the cap is
	// deliberate, since pacing at exactly 150 puts ticks at both ends of a
	// closed 60 second window and so allows 151 requests inside it.
	requestsPerMinute = 145

	pageSize       = 100 // Asana's maximum.
	maxAttempts    = 3
	requestTimeout = 30 * time.Second

	defaultRetryAfter = time.Second
	maxRetryAfter     = time.Minute

	// Caps on how much of a response body we are willing to read when it is
	// not the payload we asked for.
	maxDrainBytes = 1 << 20
	maxErrorBody  = 1 << 20

	// noRetry distinguishes a final error from a retry after no delay.
	noRetry = -1
)

type Client struct {
	baseURL   string
	http      *http.Client
	limiter   *rate.Limiter
	token     string
	workspace string
	log       *slog.Logger
}

func New(token, workspace string, log *slog.Logger) *Client {
	return &Client{
		baseURL:   defaultBaseURL,
		http:      &http.Client{Timeout: requestTimeout},
		limiter:   rate.NewLimiter(rate.Every(time.Minute/requestsPerMinute), 1),
		token:     token,
		workspace: workspace,
		log:       log,
	}
}

// Users returns every user in the workspace.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	return fetchAll[User](ctx, c, "/users", url.Values{"opt_fields": {userFields}})
}

// Projects returns every project in the workspace, archived ones included.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	return fetchAll[Project](ctx, c, "/projects", url.Values{"opt_fields": {projectFields}})
}

// page is the envelope Asana wraps every list response in.
type page[T any] struct {
	Data     []T `json:"data"`
	NextPage *struct {
		Offset string `json:"offset"`
	} `json:"next_page"`
}

// fetchAll walks every page of a list endpoint. Asana's offset token is opaque
// and single use, so the walk has to be sequential.
func fetchAll[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	q.Set("workspace", c.workspace)
	q.Set("limit", strconv.Itoa(pageSize))

	seen := map[string]bool{}
	var all []T
	for {
		var p page[T]
		if err := c.do(ctx, path, q, &p); err != nil {
			return nil, fmt.Errorf("GET %s: %w", path, err)
		}
		all = append(all, p.Data...)

		if p.NextPage == nil || p.NextPage.Offset == "" {
			return all, nil
		}
		// Any repeat, not just a consecutive one, means the server is cycling
		// offsets; the walk would otherwise spin at full request rate.
		if seen[p.NextPage.Offset] {
			return nil, fmt.Errorf("GET %s: pagination stalled at offset %s", path, p.NextPage.Offset)
		}
		seen[p.NextPage.Offset] = true
		q.Set("offset", p.NextPage.Offset)
	}
}

// do issues a GET and decodes the response into dest, retrying the failures
// that are worth retrying.
//
// Retries live here rather than in an http.RoundTripper because
// http.Client.Timeout spans a whole Do call: a Retry-After wait inside the
// transport would be charged against the request timeout and cut short.
func (c *Client) do(ctx context.Context, path string, q url.Values, dest any) error {
	endpoint := c.baseURL + path + "?" + q.Encode()

	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}

		var wait time.Duration
		wait, err = c.try(ctx, endpoint, dest, attempt)
		if err == nil {
			return nil
		}
		if wait == noRetry || attempt == maxAttempts {
			return err
		}

		c.log.Warn("retrying asana request", "path", path, "attempt", attempt, "wait", wait, "err", err)
		if serr := sleep(ctx, wait); serr != nil {
			return serr
		}
	}
	return err
}

// try makes one request. It returns how long to wait before another attempt,
// or noRetry when the error is final.
func (c *Client) try(ctx context.Context, endpoint string, dest any, attempt int) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return noRetry, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return noRetry, err
		}
		return backoff(attempt), err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			var syntax *json.SyntaxError
			var mismatch *json.UnmarshalTypeError
			if errors.As(err, &syntax) || errors.As(err, &mismatch) {
				return noRetry, fmt.Errorf("decoding response: %w", err)
			}
			// Anything else means the read failed part way through the body,
			// which another attempt may well get past.
			return backoff(attempt), fmt.Errorf("reading response: %w", err)
		}
		drain(resp.Body)
		return noRetry, nil
	}

	apiErr := parseError(resp)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return retryAfter(resp.Header.Get("Retry-After")), apiErr
	case resp.StatusCode >= 500:
		return backoff(attempt), apiErr
	default:
		return noRetry, apiErr
	}
}

// baseBackoff is a variable so tests need not wait seconds.
var baseBackoff = time.Second

// backoff grows 1s, 2s, 4s across attempts.
func backoff(attempt int) time.Duration {
	return baseBackoff << (attempt - 1)
}

// retryAfter reads the header sent with a 429. Asana documents seconds, but an
// intermediary in front of it may send the RFC 7231 date form instead, and
// falling back to a one second default there would burn every attempt in
// moments.
func retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)

	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds < 0 {
			return defaultRetryAfter
		}
		// Clamp before converting: seconds * time.Second overflows int64 into
		// a negative duration for a large enough header.
		if seconds > int(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(header); err == nil {
		return clampWait(time.Until(date))
	}
	return defaultRetryAfter
}

// clampWait keeps a wait inside [0, maxRetryAfter]; a date already in the past
// means retry now, not never.
func clampWait(d time.Duration) time.Duration {
	return min(max(d, 0), maxRetryAfter)
}

// sleep waits for d unless ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// APIError is a non-2xx response from Asana.
type APIError struct {
	Status   int
	Messages []string
}

func (e *APIError) Error() string {
	detail := strings.Join(e.Messages, "; ")
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	switch e.Status {
	case http.StatusUnauthorized:
		detail += " (check ASANA_TOKEN)"
	case http.StatusPaymentRequired:
		detail += " (a requested field needs a paid Asana plan)"
	}
	return fmt.Sprintf("asana: %d: %s", e.Status, detail)
}

// parseError reads Asana's {"errors":[{"message":...}]} body, and drains what
// is left so the connection can be reused.
func parseError(resp *http.Response) *APIError {
	apiErr := &APIError{Status: resp.StatusCode}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	drain(resp.Body)
	if err != nil {
		return apiErr
	}

	var parsed struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// Not Asana's shape at all, so probably an HTML page from something in
		// between. A snippet of it beats a bare status code.
		if s := snippet(body); s != "" {
			apiErr.Messages = []string{s}
		}
		return apiErr
	}
	for _, e := range parsed.Errors {
		apiErr.Messages = append(apiErr.Messages, e.Message)
	}
	return apiErr
}

// snippet collapses a body to something that fits on one log line.
func snippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// drain empties the body so the connection returns to the idle pool instead of
// being closed. The cap keeps a server that never stops writing from stalling
// us until the request timeout.
func drain(r io.Reader) { _, _ = io.Copy(io.Discard, io.LimitReader(r, maxDrainBytes)) }
