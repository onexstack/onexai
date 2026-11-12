// Command mcpctl is the operator tool for the MCP-server rows and the agent
// grants that point at them. Those rows live in the live DB's agent.mcp /
// agent.agent tables (onexai_agent_mcp / onexai_agent_agent on MySQL — the
// models' TableName resolves both via pkg/dbschema), not in a config file, so
// this is how they are created, inspected and re-pointed (see
// ~/.onexai/oai-agent-apiserver.mcp-tunnel.sh, which drives it for the
// local-port-forward setup).
//
// It lives under hack/ rather than cmd/ on purpose: the Makefile derives its
// binary list from cmd/*, so a tool there would be built and shipped as if it
// were a product binary.
//
// It reads the same config file oai-agent-apiserver reads (via the same app framework
// and options struct), so the DB credentials never have to be typed on a command
// line, copied into the transcript, or echoed: the tool prints only the target
// host/database and never a password, header value, or token.
//
// Modes are selected by the MODE environment variable, NOT by flags, so they
// cannot collide with the ServerOptions flag set:
//
//	show                 print the MCP row and the agent row, redacted (default)
//	apply                upsert the MCP row described by the env vars below
//	set-url=URL          set the row's url
//	set-status=STATUS     set the row's status ("enabled"/"disabled")
//	clear-headers        blank the row's headers
//	set-mcps=a,b         replace the named agent's mcps with exactly this list
//	add-mcp=NAME         union NAME into the named agent's mcps
//	del-mcp=NAME         remove NAME from the named agent's mcps
//	set-require-approval=a,b  make these tools of this server need approval
//	clear-require-approval    lift the gate from every tool of this server
//
// Other env vars: MCP_NAME (default "zadig-mcp"), AGENT (default "default").
//
// set-require-approval is how a human-in-the-loop gate is installed: the
// patterns are the tool names the MODEL sees, which is what agent matches
// (it sanitises each wire name before exposing it) and what the approval card
// shows. Listing `pipeline`, for instance, holds the chat's /pipeline
// tool until someone approves it. The grammar is fixed — an exact name, a
// trailing-`*` prefix, or `*` — and is defined in pkg/approval, which is also
// where the union with the agent-level and server-wide sources happens.
//
// apply takes the row's contents from the environment so it can register any
// server, not just zadig. Every variable defaults to the zadig row's value, so
// a bare `MODE=apply` still means exactly what it always meant:
//
//	MCP_URL            default: the in-cluster zadig service
//	MCP_DISPLAY_NAME   default "Zadig MCP"
//	MCP_DESCRIPTION    default: the zadig description
//	MCP_TAGS           comma-separated, default "zadig,devops"
//	MCP_ALLOWED_TOOLS  comma-separated; empty (default) = every tool exposed
//	MCP_AUTH           "bearer" (default) reads the token from stdin and writes
//	                   an Authorization header; "none" writes no header at all,
//	                   which has to be asked for by name
//
// Examples (run by a human, token never entering the transcript):
//
//	read -rs ZADIG_TOKEN && printf '%s' "$ZADIG_TOKEN" | \
//	  MODE=apply go run ./hack/mcpctl
//
//	# A tokenless server on a loopback port, registered under its own name.
//	MCP_NAME=oai-chat-apiserver MCP_AUTH=none MCP_URL=http://127.0.0.1:8091/mcp \
//	  MCP_DISPLAY_NAME="OneXAI Chat" MCP_TAGS=onexai,chat \
//	  MODE=apply go run ./hack/mcpctl
//
//	# Hold /pipeline for approval, then read the gate back.
//	MCP_NAME=oai-chat-apiserver MODE=set-require-approval=pipeline go run ./hack/mcpctl
//	MCP_NAME=oai-chat-apiserver MODE=show go run ./hack/mcpctl
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/app"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/cmd/oai-agent-apiserver/app/options"
	"github.com/onexstack/onexai/internal/agent/model"
)

const (
	defaultMCPName = "zadig-mcp"
	defaultAgent   = "default"
	// zadigURL is the in-cluster address. Cluster-internal DNS, so a local
	// oai-agent-apiserver can only reach it through a port-forward, which is why
	// set-url exists.
	zadigURL         = "http://zadig.example.com/mcp"
	zadigDisplayName = "Zadig MCP"
	zadigDescription = "Zadig CI/CD & DevOps MCP server (deployed in devops). Tools are action-based CRUD including workflow runs and deployments; destructive actions require a second call carrying confirm_final / is_delete=true."
	zadigTags        = "zadig,devops"
)

func main() {
	opts := options.NewServerOptions()
	a := app.NewApp("tmpzadig", "one-shot operator tool for the zadig MCP row",
		app.WithOptions(opts),
		app.WithRun(run(opts)),
		// Read ~/.onexai/oai-agent-apiserver.yaml, exactly as the server does.
		app.WithDirInHome(".onexai"),
		app.WithConfigName("oai-agent-apiserver"),
		app.WithSilence(),
		app.WithNoVersion(),
	)
	a.Run()
}

func run(opts *options.ServerOptions) app.RunFunc {
	return func(_ context.Context) error {
		db, err := opts.CoreDBOptions.NewDB()
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		fmt.Printf("db=%s/%s\n", opts.CoreDBOptions.Addr, opts.CoreDBOptions.Database)

		mode := envOr("MODE", "show")
		name := envOr("MCP_NAME", defaultMCPName)
		agent := envOr("AGENT", defaultAgent)

		switch {
		case mode == "show":
			return show(db, name, agent)
		case mode == "apply":
			return apply(db, name, agent)
		case mode == "clear-headers":
			return patchMCP(db, name, map[string]any{"headers": "", "updated_at": nowMillis()})
		case strings.HasPrefix(mode, "set-url="):
			return patchMCP(db, name, map[string]any{
				"url": strings.TrimPrefix(mode, "set-url="), "updated_at": nowMillis()})
		case strings.HasPrefix(mode, "set-status="):
			return patchMCP(db, name, map[string]any{
				"status": strings.TrimPrefix(mode, "set-status="), "updated_at": nowMillis()})
		case strings.HasPrefix(mode, "set-require-approval="):
			return setRequireApproval(db, name, splitList(strings.TrimPrefix(mode, "set-require-approval=")))
		case mode == "clear-require-approval":
			return setRequireApproval(db, name, nil)
		case strings.HasPrefix(mode, "set-mcps="):
			return setMCPs(db, agent, splitList(strings.TrimPrefix(mode, "set-mcps=")), true)
		case strings.HasPrefix(mode, "add-mcp="):
			return setMCPs(db, agent, []string{strings.TrimPrefix(mode, "add-mcp=")}, false)
		case strings.HasPrefix(mode, "del-mcp="):
			return delMCP(db, agent, strings.TrimPrefix(mode, "del-mcp="))
		default:
			return fmt.Errorf("unknown MODE %q", mode)
		}
	}
}

// apply upserts one MCP row from the environment (see the package comment for
// the variables) and unions it into the agent's mcps. A bearer token, when one
// applies, is read from stdin and never printed: only its presence and length
// class are reported.
func apply(db *gorm.DB, name, agent string) error {
	headers, err := headersForAuth()
	if err != nil {
		return err
	}

	url := envOr("MCP_URL", zadigURL)
	displayName := envOr("MCP_DISPLAY_NAME", zadigDisplayName)
	description := envOr("MCP_DESCRIPTION", zadigDescription)

	tags, err := json.Marshal(splitList(envOr("MCP_TAGS", zadigTags)))
	if err != nil {
		return err
	}
	// allowed_tools is written as "" — not "[]" — when no restriction is asked
	// for, because that is the value the reader treats as "no whitelist".
	allowedTools := ""
	if list := splitList(os.Getenv("MCP_ALLOWED_TOOLS")); len(list) > 0 {
		enc, err := json.Marshal(list)
		if err != nil {
			return err
		}
		allowedTools = string(enc)
	}

	// Raw INSERT rather than GORM's Create: the record's CreatedAt/UpdatedAt are
	// plain int64 fields, which GORM manages *by convention* in unix SECONDS,
	// and this table's convention is milliseconds. Writing the statement out
	// keeps both timestamps under our control. The table name comes from the
	// model's TableName() (agent.mcp on PostgreSQL, onexai_agent_mcp on MySQL),
	// and the conflict clause follows the dialect — MySQL's ON DUPLICATE KEY
	// UPDATE has no PostgreSQL equivalent — so the same tool works against
	// either. Column names come from the gorm tags on model.MCPM; a
	// typo here fails loudly.
	const columns = `INSERT INTO %s
	    (name, display_name, description, status, mode, protocol, url,
	     headers, allowed_tools, tags, created_at, updated_at)
	  VALUES (?, ?, ?, 'enabled', 'direct', 'HTTP', ?, ?, ?, ?, ?, ?)`
	conflict := `
	  ON DUPLICATE KEY UPDATE
	    display_name = VALUES(display_name), description = VALUES(description),
	    status = 'enabled', mode = VALUES(mode), protocol = VALUES(protocol),
	    url = VALUES(url), headers = VALUES(headers), allowed_tools = VALUES(allowed_tools),
	    tags = VALUES(tags), updated_at = VALUES(updated_at)`
	if db.Dialector.Name() == "postgres" {
		conflict = `
	  ON CONFLICT (name) DO UPDATE SET
	    display_name = EXCLUDED.display_name, description = EXCLUDED.description,
	    status = 'enabled', mode = EXCLUDED.mode, protocol = EXCLUDED.protocol,
	    url = EXCLUDED.url, headers = EXCLUDED.headers,
	    allowed_tools = EXCLUDED.allowed_tools, tags = EXCLUDED.tags,
	    updated_at = EXCLUDED.updated_at`
	}
	upsert := fmt.Sprintf(columns, model.MCPM{}.TableName()) + conflict

	ms := nowMillis()
	// Argument order follows the statement: url, headers, allowed_tools, tags,
	// created_at, updated_at.
	err = db.Exec(upsert, name, displayName, description,
		url, headers, allowedTools, string(tags), ms, ms).Error
	if err != nil {
		return fmt.Errorf("upsert mcp row: %w", err)
	}
	fmt.Printf("mcp row upserted name=%s url=%s allowed_tools=%s tags=%s\n",
		name, url, quotedOrNone(allowedTools), string(tags))

	// The MCP row alone grants nothing: scope comes from the agent's mcps.
	// The union form is deliberate — an agent that already lists other servers
	// must come out of this with all of them.
	return setMCPs(db, agent, []string{name}, false)
}

// headersForAuth builds the row's headers JSON according to MCP_AUTH.
//
// "none" has to be asked for by name rather than being what an empty stdin
// produces: a pipe that failed would otherwise silently blank the Authorization
// header on a row that needs it, and the row would keep working only until the
// server noticed.
func headersForAuth() (string, error) {
	switch auth := envOr("MCP_AUTH", "bearer"); auth {
	case "bearer":
		token, err := readToken()
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(map[string]string{"Authorization": "Bearer " + token})
		if err != nil {
			return "", err
		}
		fmt.Printf("auth: bearer token from stdin (len=%d)\n", len(token))
		return string(b), nil
	case "none":
		fmt.Println("auth: none — the row will carry no Authorization header")
		return "", nil
	default:
		return "", fmt.Errorf("MCP_AUTH=%q is neither \"bearer\" nor \"none\"", auth)
	}
}

// quotedOrNone renders an allowed_tools value for the one-line report, saying
// "all" rather than an empty string so the report is not mistaken for a bug.
func quotedOrNone(v string) string {
	if v == "" {
		return "all"
	}
	return v
}

// show prints both rows in redacted form: a header value is never emitted, only
// its scheme and whether a value is present.
func show(db *gorm.DB, name, agent string) error {
	var m struct {
		Name, Status, Mode, Protocol, URL, Headers, AllowedTools, Tags string
		RequireApproval                                                string
	}
	if err := db.Model(&model.MCPM{}).
		Select("name, status, mode, protocol, url, headers, allowed_tools, require_approval, tags").
		Where("name = ?", name).Scan(&m).Error; err != nil {
		return fmt.Errorf("read mcp row: %w", err)
	}
	if m.Name == "" {
		fmt.Printf("mcp row %q: ABSENT\n", name)
	} else {
		fmt.Printf("mcp row %q:\n  status=%s mode=%s protocol=%s\n  url=%s\n  allowed_tools=%q tags=%s\n",
			m.Name, m.Status, m.Mode, m.Protocol, m.URL, m.AllowedTools, m.Tags)
		fmt.Printf("  require_approval=%s\n", gated(m.RequireApproval))
		redactHeaders(m.Headers)
	}

	// Read through the real record: the column names come from its GORM tags
	// (MCPs -> column:mcps) and the table from TableName(), the same model the
	// service writes with, so the two cannot drift.
	var a model.AgentM
	if err := db.Model(&model.AgentM{}).
		Select("name, mcps, status, updated_at").
		Where("name = ?", agent).Scan(&a).Error; err != nil {
		return fmt.Errorf("read agent row: %w", err)
	}
	if a.Name == "" {
		fmt.Printf("agent row %q: ABSENT\n", agent)
		return nil
	}
	fmt.Printf("agent row %q: status=%s updated_at=%d\n  mcps=%s\n",
		a.Name, a.Status, a.UpdatedAt, listOrNone(a.MCPs))

	// Report every MCP row so a stale name in mcps is obvious, with the gate
	// alongside it: the deployment's whole approval policy is these patterns
	// unioned with the agent rows', so it is worth being able to read at a
	// glance which server holds which tools.
	var all []model.MCPM
	if err := db.Select("name, status, protocol, url, require_approval").Find(&all).Error; err != nil {
		return fmt.Errorf("list mcp rows: %w", err)
	}
	fmt.Printf("all mcp rows (%d):\n", len(all))
	for _, r := range all {
		fmt.Printf("  %-20s status=%-9s protocol=%-5s url=%s require_approval=%s\n",
			r.Name, r.Status, r.Protocol, r.URL, listOrNone(r.RequireApproval))
	}
	return nil
}

// gated renders a row's approval patterns, saying "none" rather than leaving a
// blank so the line cannot be read as "the query did not fetch it".
func gated(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return "none"
	}
	return raw
}

// listOrNone renders a StringList column for the one-line report, for the same
// reason gated does: an empty list and a value the query did not fetch must not
// look alike.
func listOrNone(v model.StringList) string {
	if len(v) == 0 {
		return "none"
	}
	return strings.Join(v, ",")
}

// redactHeaders reports the shape of the headers JSON without its values.
func redactHeaders(raw string) {
	if strings.TrimSpace(raw) == "" {
		fmt.Println("  headers: EMPTY")
		return
	}
	var h map[string]string
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		fmt.Printf("  headers: NOT VALID JSON (%d bytes, err=%v)\n", len(raw), err)
		return
	}
	for k, v := range h {
		scheme, rest, found := strings.Cut(v, " ")
		if !found {
			// No space: the whole value is one opaque token.
			fmt.Printf("  header %s: opaque present=%t len=%d\n", k, v != "", len(v))
			continue
		}
		fmt.Printf("  header %s: scheme=%s present=%t len=%d\n", k, scheme, rest != "", len(rest))
	}
}

// setRequireApproval rewrites the tool-name patterns this row must get
// approval for. An empty list is how a gate is lifted.
//
// It is a separate mode from apply on purpose. apply upserts a row from the
// environment, and it does not carry this column at all: re-pointing a server's
// url — or re-registering it after a port-forward moved — must not quietly
// ungate the tools somebody deliberately gated. Lifting a gate therefore has to
// be asked for by name, in the mode that says so.
func setRequireApproval(db *gorm.DB, name string, patterns []string) error {
	// "" rather than "[]": that is the value the reader treats as "no gate", and
	// the same one apply leaves behind for a freshly inserted row.
	encoded := ""
	if len(patterns) > 0 {
		b, err := json.Marshal(patterns)
		if err != nil {
			return fmt.Errorf("encode require_approval: %w", err)
		}
		encoded = string(b)
	}
	if err := patchMCP(db, name, map[string]any{
		"require_approval": encoded, "updated_at": nowMillis()}); err != nil {
		return err
	}
	if encoded == "" {
		fmt.Printf("mcp row %q gates nothing: every tool of this server runs without approval\n", name)
		return nil
	}
	fmt.Printf("mcp row %q holds these tools for approval: %s\n", name, strings.Join(patterns, ", "))
	return nil
}

// patchMCP updates the named MCP row and reports whether it existed.
func patchMCP(db *gorm.DB, name string, values map[string]any) error {
	tx := db.Model(&model.MCPM{}).Where("name = ?", name).Updates(values)
	if tx.Error != nil {
		return fmt.Errorf("update mcp row: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return fmt.Errorf("no mcp row named %q (nothing updated) — run MODE=apply first", name)
	}
	fmt.Printf("mcp row %q updated: %v\n", name, keys(values))
	return nil
}

// setMCPs rewrites an agent's mcps, either to exactly the given list or as a
// union with what is already there. The read-then-write is the point: an agent
// that already lists other servers must not lose them.
func setMCPs(db *gorm.DB, agent string, add []string, exact bool) error {
	var row model.AgentM
	if err := db.Where("name = ?", agent).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("no agent row named %q; this tool deliberately does not create agents", agent)
		}
		return fmt.Errorf("read agent row: %w", err)
	}

	// row.MCPs is already decoded: model.StringList reads the column itself, so
	// there is no JSON to unmarshal here. It reads an unparsable value as an
	// empty list rather than failing, which is how the service reads the same
	// row — this tool no longer rejects a value the server would accept.
	before := append([]string(nil), row.MCPs...)
	cur := append([]string(nil), row.MCPs...)

	if exact {
		cur = nil
	}
	seen := make(map[string]bool, len(cur)+len(add))
	for _, n := range cur {
		seen[n] = true
	}
	for _, n := range add {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		cur = append(cur, n)
	}

	if len(cur) == len(before) && strings.Join(cur, ",") == strings.Join(before, ",") {
		fmt.Printf("agent %q mcps already %v; no change\n", agent, cur)
		return nil
	}

	// A map rather than a struct: an emptied list is the zero StringList, and
	// Updates skips zero-valued struct fields, so the one case that must write
	// the empty encoding would be the one case that did not.
	if err := db.Model(&model.AgentM{}).Where("name = ?", agent).
		Updates(map[string]any{"mcps": model.StringList(cur), "updated_at": nowMillis()}).Error; err != nil {
		return fmt.Errorf("update agent row: %w", err)
	}
	fmt.Printf("agent %q mcps: %v -> %v\n", agent, before, cur)
	return nil
}

// delMCP removes one server from an agent's mcps.
func delMCP(db *gorm.DB, agent, remove string) error {
	var row model.AgentM
	if err := db.Where("name = ?", agent).First(&row).Error; err != nil {
		return fmt.Errorf("read agent row: %w", err)
	}
	kept := make([]string, 0, len(row.MCPs))
	for _, n := range row.MCPs {
		if n != remove {
			kept = append(kept, n)
		}
	}
	if err := db.Model(&model.AgentM{}).Where("name = ?", agent).
		Updates(map[string]any{"mcps": model.StringList(kept), "updated_at": nowMillis()}).Error; err != nil {
		return fmt.Errorf("update agent row: %w", err)
	}
	fmt.Printf("agent %q mcps: %v -> %v\n", agent, row.MCPs, kept)
	return nil
}

// readToken reads the API token from stdin, refusing an empty or truncated one
// so a failed pipe cannot silently write an unusable row.
func readToken() (string, error) {
	b, err := os.ReadFile("/dev/stdin")
	if err != nil {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token on stdin is empty (did the pipe fail?)")
	}
	if len(tok) < 16 {
		return "", fmt.Errorf("token on stdin is only %d bytes; refusing to write a probably-truncated value", len(tok))
	}
	return tok, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
