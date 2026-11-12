// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package regen implements the duty provider contract against the Regen
// on-call API.
//
// Regen has no API key. Authentication is a cookie session: POST
// /api/v1/auth/login with an email and password returns an HttpOnly
// "oi_session" cookie that lasts seven days. The client therefore keeps a
// cookie jar and re-logs in when the server answers 401.
package regen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"resty.dev/v3"

	"golang.org/x/sync/singleflight"

	"github.com/onexstack/onexai/pkg/duty"
)

// Client implements duty.Provider against a Regen server.
type Client struct {
	baseURL  string
	username string
	password string
	client   *resty.Client

	// loggedIn reports whether a session cookie is believed valid. It is atomic
	// because readers must not block behind a login in flight.
	loggedIn atomic.Bool
	// loginSF de-duplicates concurrent logins. The HTTP round trip runs outside
	// any lock: holding a mutex across it would serialize every other request
	// in the process behind a slow provider.
	loginSF singleflight.Group
}

var (
	_ duty.Provider          = (*Client)(nil)
	_ duty.TransitionChecker = (*Client)(nil)
)

// wire types mirror the Regen JSON payloads. They stay unexported: callers see
// only the neutral duty types.
type wireSchedule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
}

type wireOnCall struct {
	ScheduleID string `json:"schedule_id"`
	At         string `json:"at"`
	UserName   string `json:"user_name"`
	IsOverride bool   `json:"is_override"`
}

type wireIncident struct {
	ID             string `json:"id"`
	IncidentNumber int    `json:"incident_number"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Severity       string `json:"severity"`
	Description    string `json:"description"`
	CreatedAt      string `json:"created_at"`
	ResolvedAt     string `json:"resolved_at"`
}

type wireCreateIncident struct {
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Description string `json:"description,omitempty"`
}

type wireUpdateIncident struct {
	Status   string `json:"status,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// NewClient creates a Regen provider. When endpoint is empty it returns a
// disabled provider, so a deployment without Regen gets a readable
// "not configured" answer rather than a nil dereference.
//
// username is the account's login email and password is its secret; neither is
// ever logged.
func NewClient(endpoint, username, password string) duty.Provider {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return duty.NewNoop("regen")
	}

	// cookiejar.New only errors when the options are malformed, and there are
	// none here; the fallback keeps the signature free of an error return.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return duty.NewNoop("regen")
	}

	return &Client{
		baseURL:  strings.TrimRight(endpoint, "/"),
		username: strings.TrimSpace(username),
		password: password,
		client:   resty.New().SetTimeout(30 * time.Second).SetCookieJar(jar),
	}
}

// Name returns the provider name stored as a ticket's source.
func (c *Client) Name() string { return "regen" }

// Configured reports whether an endpoint was configured.
func (c *Client) Configured() bool { return true }

// Schedules returns every schedule. The response is enveloped as
// {"data": [...], "total": N}.
func (c *Client) Schedules(ctx context.Context) ([]duty.Schedule, error) {
	var out struct {
		Data []wireSchedule `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/schedules", nil, &out); err != nil {
		return nil, err
	}
	schedules := make([]duty.Schedule, 0, len(out.Data))
	for _, s := range out.Data {
		schedules = append(schedules, duty.Schedule{ID: s.ID, Name: s.Name})
	}
	return schedules, nil
}

// OnCall returns the current on-call person for a schedule.
func (c *Client) OnCall(ctx context.Context, scheduleID string) (duty.Person, error) {
	var out wireOnCall
	path := "/api/v1/schedules/" + url.PathEscape(scheduleID) + "/oncall"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return duty.Person{}, err
	}
	// Regen stores participants as free text (an email), so the name doubles as
	// the identity until the chat layer maps it to a Lark user id.
	return duty.Person{ID: out.UserName, Name: out.UserName}, nil
}

// CreateIncident opens an incident.
func (c *Client) CreateIncident(ctx context.Context, in duty.CreateIncidentRequest) (duty.Incident, error) {
	var out wireIncident
	req := wireCreateIncident{
		Title:       in.Title,
		Severity:    string(in.Severity),
		Description: in.Description,
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/incidents", req, &out); err != nil {
		return duty.Incident{}, err
	}
	return toIncident(out), nil
}

// GetIncident returns an incident's current state.
func (c *Client) GetIncident(ctx context.Context, id string) (duty.Incident, error) {
	var out wireIncident
	if err := c.do(ctx, http.MethodGet, "/api/v1/incidents/"+url.PathEscape(id), nil, &out); err != nil {
		return duty.Incident{}, err
	}
	return toIncident(out), nil
}

// UpdateIncident patches an incident.
func (c *Client) UpdateIncident(ctx context.Context, id string, in duty.UpdateIncidentRequest) (duty.Incident, error) {
	var out wireIncident
	req := wireUpdateIncident{Status: string(in.Status), Severity: string(in.Severity)}
	if err := c.do(ctx, http.MethodPatch, "/api/v1/incidents/"+url.PathEscape(id), req, &out); err != nil {
		return duty.Incident{}, err
	}
	return toIncident(out), nil
}

// CanTransition mirrors Regen's one-way incident state machine:
//
//	triggered    -> acknowledged, resolved, canceled
//	acknowledged -> resolved
//	resolved     -> (terminal)
//	canceled     -> (terminal)
func (c *Client) CanTransition(from, to duty.Status) bool {
	switch from {
	case duty.StatusTriggered:
		return to == duty.StatusAcknowledged || to == duty.StatusResolved || to == duty.StatusCanceled
	case duty.StatusAcknowledged:
		return to == duty.StatusResolved
	default:
		return false
	}
}

func toIncident(in wireIncident) duty.Incident {
	return duty.Incident{
		ID:       in.ID,
		Status:   duty.Status(in.Status),
		Severity: duty.Severity(in.Severity),
	}
}

// do sends a request, authenticating first and re-authenticating once if the
// session has expired.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	if err := c.ensureSession(ctx); err != nil {
		return err
	}

	raw, status, err := c.sendResilient(ctx, method, path, in)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		// The session cookie expired (it lasts seven days) or was revoked.
		if err := c.relogin(ctx); err != nil {
			return err
		}
		raw, status, err = c.sendResilient(ctx, method, path, in)
		if err != nil {
			return err
		}
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("regen: %s %s: status %d: %s", method, path, status, truncate(raw, 512))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("regen: decode %s %s response: %w", method, path, err)
	}
	return nil
}

// transportRetryDelay is how long sendResilient waits before retrying a request
// whose transport failed. It is short on purpose: the failures it covers are
// blips on the way to the backend — a dial that lost a route, a name that did
// not resolve — and the caller is waiting for the answer.
const transportRetryDelay = 200 * time.Millisecond

// sendResilient sends a request, retrying a transport failure once when the
// request is safe to send again.
//
// A request with side effects is never replayed, because the first attempt may
// have reached the server. Only GET and HEAD are retried.
func (c *Client) sendResilient(ctx context.Context, method, path string, in any) ([]byte, int, error) {
	raw, status, err := c.send(ctx, method, path, in)
	if err == nil || !replayable(method) {
		return raw, status, err
	}
	// A cancelled caller must not be made to wait — or retried.
	if ctx.Err() != nil {
		return nil, 0, err
	}

	timer := time.NewTimer(transportRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, 0, err
	case <-timer.C:
	}
	return c.send(ctx, method, path, in)
}

// replayable reports whether a request may be sent a second time after a
// transport failure without risking a duplicate side effect.
func replayable(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// ensureSession logs in when no session has been established yet. Concurrent
// callers share one login through singleflight.
func (c *Client) ensureSession(ctx context.Context) error {
	if c.loggedIn.Load() {
		return nil
	}
	_, err, _ := c.loginSF.Do("regen-login", func() (any, error) {
		if c.loggedIn.Load() {
			return nil, nil
		}
		return nil, c.login(ctx)
	})
	return err
}

// relogin discards the current session and logs in again.
//
// The cookie jar is cleared in place rather than replaced: the jar's API is
// safe for concurrent use, while assigning a new *Jar to the client races with
// requests already in flight.
func (c *Client) relogin(ctx context.Context) error {
	c.loggedIn.Store(false)
	if u, err := url.Parse(c.baseURL); err == nil && c.client.CookieJar() != nil {
		c.client.CookieJar().SetCookies(u, nil)
	}
	_, err, _ := c.loginSF.Do("regen-login", func() (any, error) {
		if c.loggedIn.Load() {
			return nil, nil
		}
		return nil, c.login(ctx)
	})
	return err
}

// login performs the login exchange.
func (c *Client) login(ctx context.Context) error {
	if c.username == "" {
		return fmt.Errorf("regen: username is not configured")
	}

	raw, status, err := c.send(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email":    c.username,
		"password": c.password,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		// The body never carries the password, so it is safe to surface.
		return fmt.Errorf("regen: login as %s failed: status %d: %s", c.username, status, truncate(raw, 512))
	}
	c.loggedIn.Store(true)
	return nil
}

// send performs one HTTP round trip. It assumes the session cookie is already
// in the jar and does not authenticate.
func (c *Client) send(ctx context.Context, method, path string, in any) ([]byte, int, error) {
	endpoint := c.baseURL + path

	req := c.client.R().SetContext(ctx)
	if in != nil {
		req.SetBody(in)
	}

	resp, err := req.SetResponseDoNotParse(true).Execute(method, endpoint)
	if err != nil {
		return nil, 0, fmt.Errorf("regen: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode(), fmt.Errorf("regen: read %s %s response: %w", method, path, err)
	}

	// The credentials are deliberately absent from this line; only the response
	// side is ever logged.
	slog.DebugContext(ctx, "regen: response",
		"url", endpoint,
		"method", method,
		"status", resp.StatusCode(),
		"body_preview", truncate(raw, 512),
	)

	return raw, resp.StatusCode(), nil
}

// truncate returns at most maxLen bytes of raw, appending "..." if truncated.
func truncate(raw []byte, maxLen int) string {
	if len(raw) <= maxLen {
		return string(raw)
	}
	return string(raw[:maxLen]) + "..."
}
