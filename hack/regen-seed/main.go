// Command regen-seed creates the Regen fixtures the chat duty commands
// are exercised against: one weekly-rotation schedule per duty module and the
// engineers that rotate through them.
//
// It reads and writes the live Regen instance through its HTTP API, so it needs
// an admin account; Regen has no API key, so the tool logs in and keeps the
// session cookie itself. The account is taken from the same config file
// oai-chat-apiserver reads ($HOME/.onexai/oai-chat-apiserver.yaml, backend.regen-*),
// which is where the deployment keeps it anyway — no credential has to be
// exported, typed on a command line, or echoed into a transcript. Every setting
// can be overridden from the environment when seeding some other instance:
//
//	REGEN_URL             default: backend.regen-endpoint from the config
//	REGEN_EMAIL           default: backend.regen-username
//	REGEN_PASSWORD        default: backend.regen-password
//	REGEN_MODULES         default "onex,obs,go,kubernetes,aiinfra"
//	REGEN_TEAM            default "go.kong@example.com,toka.wu@example.com,ops.luo@example.com",
//	                      in rotation order — the first name takes this week
//	REGEN_TEAM_PASSWORD   required only when a user has to be created; Regen
//	                      refuses a password shorter than 8 characters
//	REGEN_STAGGER         "1" offsets each module's rotation by a week, so the
//	                      modules do not all share one on-call person
//
// It is idempotent: a user or schedule that already exists is left alone, so
// re-running after adding a module only creates the new one. The rotation
// starts on the most recent Monday at 00:00 UTC (less the stagger offset),
// which is what makes someone on call right now — a start date in the future
// would leave every schedule with nobody on call until it arrived.
//
// It lives under hack/ rather than cmd/ for the same reason mcpctl does: the
// Makefile derives its binary list from cmd/*, so a tool there would be built
// and shipped as if it were a product binary.
//
// Example (via a port-forward, when the cluster address is not routable):
//
//	kubectl -n observability port-forward svc/regen 8080:8080 &
//	read -rs REGEN_PASSWORD && export REGEN_PASSWORD
//	REGEN_URL=http://127.0.0.1:8080 REGEN_EMAIL=oai-chat-apiserver@example.com \
//	  go run ./hack/regen-seed
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/viper"

	customoptions "github.com/onexstack/onexai/pkg/options"
)

// defaultModules are the duty module codes, which are also the schedule names:
// the chat matches a schedule by name to resolve a module's on-call.
const defaultModules = "onex,obs,go,kubernetes,aiinfra"

// defaultTeam is the rotation, in order.
const defaultTeam = "go.kong@example.com,toka.wu@example.com,ops.luo@example.com"

// timezone is what the schedules are created with. Regen stores UTC internally;
// this only affects how shifts are displayed.
const timezone = "Asia/Shanghai"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "regen-seed:", err)
		os.Exit(1)
	}
}

// run is main's body, returning instead of exiting so the defers above it run.
func run(ctx context.Context) error {
	cfg, err := configFromEnv()
	if err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	cli, err := newClient(ctx, cfg)
	if err != nil {
		return err
	}

	team := splitList(cfg.team)
	modules := splitList(cfg.modules)
	if len(team) == 0 || len(modules) == 0 {
		return fmt.Errorf("REGEN_TEAM and REGEN_MODULES must not be empty")
	}

	// The users first: a schedule participant is free text, but a Regen user
	// row is what lets the platform resolve who was paged.
	for _, email := range team {
		if err := ensureUser(ctx, cli, email, cfg.teamPassword); err != nil {
			return err
		}
	}

	rotationStart := lastMondayUTC(time.Now())
	for i, module := range modules {
		// With staggering on, each module starts one week later in the
		// rotation than the one before it, cycling through the team: that is
		// what makes two modules have different people on call in the same
		// week, which is the interesting case to test.
		start := rotationStart
		if cfg.stagger {
			start = rotationStart.AddDate(0, 0, -7*(i%len(team)))
		}
		if err := ensureSchedule(ctx, cli, module, team, start, cfg.stagger); err != nil {
			return err
		}
	}

	// Report the assignments the way the chat will read them, so a
	// misconfigured rotation shows up here rather than in a Feishu card.
	fmt.Println("\ncurrent on-call:")
	for _, module := range modules {
		oncall, err := cli.getOnCall(ctx, module)
		if err != nil {
			return err
		}
		if oncall == "" {
			oncall = "(nobody)"
		}
		fmt.Printf("  %-12s %s\n", module, oncall)
	}
	return nil
}

// config holds the tool's settings, from the environment or the deployment's
// config file.
type config struct {
	baseURL      string
	email        string
	password     string
	modules      string
	team         string
	teamPassword string
	stagger      bool
}

// configFromEnv reads the settings, falling back to the config file
// oai-chat-apiserver reads for anything the environment does not override.
func configFromEnv() (config, error) {
	cfg := config{
		baseURL:      os.Getenv("REGEN_URL"),
		email:        os.Getenv("REGEN_EMAIL"),
		password:     os.Getenv("REGEN_PASSWORD"),
		modules:      envOr("REGEN_MODULES", defaultModules),
		team:         envOr("REGEN_TEAM", defaultTeam),
		teamPassword: os.Getenv("REGEN_TEAM_PASSWORD"),
		stagger:      envOr("REGEN_STAGGER", "") != "",
	}

	// The live config is the normal source: it is where the deployment already
	// keeps the service account, so nothing has to be exported to run this.
	fileCfg, err := configFromFile()
	if err != nil {
		return cfg, err
	}
	if cfg.baseURL == "" {
		cfg.baseURL = fileCfg.baseURL
	}
	if cfg.email == "" {
		cfg.email = fileCfg.email
	}
	if cfg.password == "" {
		cfg.password = fileCfg.password
	}
	if cfg.baseURL == "" {
		cfg.baseURL = "http://regen.example.com:8080"
	}
	return cfg, nil
}

// configFromFile reads backend.regen-* out of $HOME/.onexai/oai-chat-apiserver.yaml.
// A missing file is not an error: an operator seeding another instance sets the
// environment instead.
func configFromFile() (config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config{}, nil
	}
	path := filepath.Join(home, ".onexai", "oai-chat-apiserver.yaml")
	if _, err := os.Stat(path); err != nil {
		return config{}, nil
	}

	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return config{}, fmt.Errorf("read %s: %w", path, err)
	}

	var fileCfg struct {
		Backend customoptions.BackendOptions `mapstructure:"backend"`
	}
	if err := v.Unmarshal(&fileCfg); err != nil {
		return config{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return config{
		baseURL:  fileCfg.Backend.RegenEndpoint,
		email:    fileCfg.Backend.RegenUsername,
		password: fileCfg.Backend.RegenPassword,
	}, nil
}

// validate reports the settings that cannot be defaulted.
func (c config) validate() error {
	if c.email == "" || c.password == "" {
		return fmt.Errorf("no Regen account: set REGEN_EMAIL and REGEN_PASSWORD, or backend.regen-username/\\\n" +
			"regen-password in $HOME/.onexai/oai-chat-apiserver.yaml")
	}
	return nil
}

// client is a minimal Regen HTTP client: a cookie jar, one login, and a request
// helper. It is deliberately not the production client — the seeding endpoints
// (users, schedules) are admin-only and have no place in the chat's
// runtime surface.
type client struct {
	baseURL string
	http    *http.Client
}

func newClient(ctx context.Context, cfg config) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("build cookie jar: %w", err)
	}
	c := &client{
		baseURL: strings.TrimRight(cfg.baseURL, "/"),
		// Seeding is interactive; a hung Regen should not hang the operator.
		http: &http.Client{Timeout: 30 * time.Second, Jar: jar},
	}

	status, err := c.do(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email":    cfg.email,
		"password": cfg.password,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("login as %s failed: status %d", cfg.email, status)
	}
	fmt.Printf("logged in to %s as %s\n", c.baseURL, cfg.email)
	return c, nil
}

// do sends one request. A non-2xx status is returned to the caller rather than
// treated as an error, because a few callers act on specific codes (409 on a
// duplicate user, 403 on the user limit).
func (c *client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("marshal %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, fmt.Errorf("create %s %s: %w", method, path, err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read %s %s: %w", method, path, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, nil
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// ensureUser creates the Regen user for email unless one already exists.
func ensureUser(ctx context.Context, c *client, email, password string) error {
	var list struct {
		Users []struct {
			Email string `json:"email"`
		} `json:"users"`
	}
	status, err := c.do(ctx, http.MethodGet, "/api/v1/settings/users", nil, &list)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("list users: status %d", status)
	}
	for _, u := range list.Users {
		if strings.EqualFold(u.Email, email) {
			fmt.Printf("user   %-28s exists\n", email)
			return nil
		}
	}

	if len(password) < 8 {
		return fmt.Errorf("user %s does not exist and REGEN_TEAM_PASSWORD is missing or shorter than 8 characters", email)
	}

	status, err = c.do(ctx, http.MethodPost, "/api/v1/settings/users", map[string]string{
		"email":    email,
		"name":     localPart(email),
		"role":     "member",
		"password": password,
	}, nil)
	if err != nil {
		return fmt.Errorf("create user %s: %w", email, err)
	}
	switch status {
	case http.StatusCreated, http.StatusOK:
		fmt.Printf("user   %-28s created (password from REGEN_TEAM_PASSWORD)\n", email)
	case http.StatusConflict:
		fmt.Printf("user   %-28s exists (created concurrently)\n", email)
	case http.StatusForbidden:
		return fmt.Errorf("create user %s: the community edition user limit is reached", email)
	default:
		return fmt.Errorf("create user %s: status %d", email, status)
	}
	return nil
}

// schedule is the part of a Regen schedule this tool reads back.
type schedule struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Layers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"layers"`
}

// ensureSchedule creates the schedule for module and, when it has no layer yet,
// the weekly rotation over team. With stagger set it also re-points an existing
// rotation at rotationStart, which is how the modules are offset from each
// other after the fact.
func ensureSchedule(ctx context.Context, c *client, module string, team []string, rotationStart time.Time, stagger bool) error {
	existing, err := c.findSchedule(ctx, module)
	if err != nil {
		return err
	}

	if existing == nil {
		var created schedule
		status, err := c.do(ctx, http.MethodPost, "/api/v1/schedules", map[string]string{
			"name":        module,
			"description": "OneXAI duty rotation for " + module,
			"timezone":    timezone,
		}, &created)
		if err != nil {
			return fmt.Errorf("create schedule %s: %w", module, err)
		}
		if status != http.StatusCreated && status != http.StatusOK {
			return fmt.Errorf("create schedule %s: status %d", module, status)
		}
		fmt.Printf("schedule %-27s created\n", module)
		existing = &created
	} else {
		fmt.Printf("schedule %-27s exists\n", module)
	}

	// The list endpoint does not preload layers, so read the schedule back
	// before deciding whether the rotation is already there.
	detail, err := c.getSchedule(ctx, existing.ID)
	if err != nil {
		return err
	}
	if len(detail.Layers) > 0 {
		if !stagger {
			return nil
		}
		layer := detail.Layers[0]
		status, err := c.do(ctx, http.MethodPatch,
			"/api/v1/schedules/"+url.PathEscape(existing.ID)+"/layers/"+url.PathEscape(layer.ID),
			map[string]string{"rotation_start": rotationStart.Format(time.RFC3339)}, nil)
		if err != nil {
			return fmt.Errorf("stagger %s: %w", module, err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("stagger %s: status %d", module, status)
		}
		fmt.Printf("rotation %-26s staggered to %s UTC\n", module, rotationStart.Format(time.RFC3339))
		return nil
	}

	participants := make([]map[string]any, 0, len(team))
	for i, email := range team {
		participants = append(participants, map[string]any{"user_name": email, "order_index": i})
	}

	status, err := c.do(ctx, http.MethodPost, "/api/v1/schedules/"+url.PathEscape(existing.ID)+"/layers", map[string]any{
		"name":           "Primary",
		"order_index":    0,
		"rotation_type":  "weekly",
		"rotation_start": rotationStart.Format(time.RFC3339),
		"participants":   participants,
	}, nil)
	if err != nil {
		return fmt.Errorf("create layer for %s: %w", module, err)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return fmt.Errorf("create layer for %s: status %d", module, status)
	}
	fmt.Printf("rotation %-26s created: weekly, starting %s UTC\n", module, rotationStart.Format(time.RFC3339))
	return nil
}

// findSchedule returns the schedule named module, or nil.
func (c *client) findSchedule(ctx context.Context, module string) (*schedule, error) {
	var list struct {
		Data []schedule `json:"data"`
	}
	status, err := c.do(ctx, http.MethodGet, "/api/v1/schedules", nil, &list)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list schedules: status %d", status)
	}
	for i := range list.Data {
		if strings.EqualFold(list.Data[i].Name, module) {
			return &list.Data[i], nil
		}
	}
	return nil, nil
}

// getSchedule reads one schedule including its layers.
func (c *client) getSchedule(ctx context.Context, id string) (*schedule, error) {
	var out schedule
	status, err := c.do(ctx, http.MethodGet, "/api/v1/schedules/"+url.PathEscape(id), nil, &out)
	if err != nil {
		return nil, fmt.Errorf("get schedule %s: %w", id, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get schedule %s: status %d", id, status)
	}
	return &out, nil
}

// getOnCall returns the current on-call person of module, or an empty string.
func (c *client) getOnCall(ctx context.Context, module string) (string, error) {
	sched, err := c.findSchedule(ctx, module)
	if err != nil {
		return "", err
	}
	if sched == nil {
		return "", fmt.Errorf("schedule %s disappeared", module)
	}

	var out struct {
		UserName string `json:"user_name"`
	}
	status, err := c.do(ctx, http.MethodGet, "/api/v1/schedules/"+url.PathEscape(sched.ID)+"/oncall", nil, &out)
	if err != nil {
		return "", fmt.Errorf("on-call for %s: %w", module, err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("on-call for %s: status %d", module, status)
	}
	return out.UserName, nil
}

// lastMondayUTC returns the most recent Monday at 00:00 UTC, which is the start
// of the week the rotation is currently in. A start in the future would leave
// every schedule empty until it arrived.
func lastMondayUTC(now time.Time) time.Time {
	now = now.UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	// Go numbers Sunday 0; shift so Monday is 0.
	daysSinceMonday := (int(midnight.Weekday()) + 6) % 7
	return midnight.AddDate(0, 0, -daysSinceMonday)
}

// localPart returns the part of an email before the @, used as the display
// name: the chat's user map keys on the full address, but the Lark
// user_id these fixtures stand in for is the login name.
func localPart(email string) string {
	if name, _, ok := strings.Cut(email, "@"); ok {
		return name
	}
	return email
}

// envOr returns the environment value for key, or def when it is unset.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitList splits a comma-separated list, trimming blanks.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
