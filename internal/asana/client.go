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
	// plans. A burst of 1 spreads requests evenly, so no 60 second window can
	// hold more than the limit even across a window boundary.
	requestsPerMinute = 150

	pageSize       = 100 // Asana's maximum.
	maxAttempts    = 3
	requestTimeout = 30 * time.Second

	defaultRetryAfter = time.Second
	maxRetryAfter     = time.Minute

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
		// An offset that does not advance would loop forever, burning quota.
		if p.NextPage.Offset == q.Get("offset") {
			return nil, fmt.Errorf("GET %s: pagination stalled at offset %s", path, p.NextPage.Offset)
		}
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

// retryAfter reads the header Asana sends with a 429, in seconds.
func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds < 0 {
		return defaultRetryAfter
	}
	// Clamp before converting: seconds * time.Second overflows int64 into a
	// negative duration for a large enough header.
	if seconds > int(maxRetryAfter/time.Second) {
		return maxRetryAfter
	}
	return time.Duration(seconds) * time.Second
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
// is left so the connection can be reused. A body that is missing or not JSON
// still leaves a usable status.
func parseError(resp *http.Response) *APIError {
	apiErr := &APIError{Status: resp.StatusCode}

	var body struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err == nil {
		for _, e := range body.Errors {
			apiErr.Messages = append(apiErr.Messages, e.Message)
		}
	}
	drain(resp.Body)
	return apiErr
}

// drain empties the body so the connection returns to the idle pool instead of
// being closed.
func drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }
