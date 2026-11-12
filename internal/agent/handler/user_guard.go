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

package handler

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// errResourceNotFound is the guard's own not-found signal. It exists because
// the two store backends disagree: the SQL rows answer gorm.ErrRecordNotFound,
// while the control-plane reader answers (nil, nil). The guard only needs one
// question answered — "is there an object here to check?" — so both become this.
var errResourceNotFound = errors.New("resource not found")

// ownerlessMessage is the answer to a non-administrator trying to change a
// platform-owned row. It says "not yours" without confirming what exists.
const (
	ownerlessMessage = "只有资源归属人或管理员可以修改该资源"
	anonymousMessage = "请先登录后再修改资源"
	unavailableMsg   = "服务未配置资源存储，无法校验资源归属"
)

// UserGuard is the authorization policy of the declarative resource surface for
// browser (bearer) callers.
//
// # Why it exists
//
// UnifiedAuth authenticates any signed-in user on this surface. Authentication
// answers who the caller is; this middleware answers what they may do. It must
// run after UnifiedAuth (it reads the claims that middleware stores) and before
// the handlers.
//
// # Three decisions
//
//  1. **Governance resources stay administrator-only.** Secret management and
//     authorization rules can rewrite every permission in the deployment, so
//     they keep the pre-existing role check. They are listed once, here, rather
//     than scattered over handler bodies.
//  2. **Reads are open to every signed-in user.** The platform catalog (agents,
//     models, knowledge bases, …) is shared; a normal user must be able to
//     browse it to pick what to run.
//  3. **Writes are owner-scoped.** A resource with an `owner` column can only
//     be modified by that owner or an administrator. Creating one stamps the
//     caller as the owner server-side, so the rule does not depend on a
//     well-behaved client. Resources without an owner column (skills, MCPs,
//     prompts, …) are shared building blocks and are writable by any signed-in
//     user.
//
// # Which callers are exempt
//
//   - AK/SK service callers carry no claims. The signature is the whole
//     credential, and the service face's contract is unchanged: no claims means
//     no role check here.
//   - Administrators (`is_admin`) bypass the owner restriction. They do not
//     bypass governance; that check is above the role question.
//   - Anonymous callers may read but not write. Anonymous auth is refused at
//     startup, but a middleware that only works because the option validation
//     runs is one binary away from being wrong.
func (h *Handler) UserGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := bearer.ClaimsFrom(c)
		if !ok {
			// AK/SK service caller: nothing to enforce.
			c.Next()
			return
		}

		// Attach the request-scoped authorization view before any read is
		// served. The list business layer consults it; a write consult happens
		// below, after the governance and role questions.
		state := h.authzState(c, claims)
		if state != nil {
			c.Request = c.Request.WithContext(authzview.WithState(c.Request.Context(), state))
		}

		if isGovernancePath(c.Request.URL.Path) {
			if !bearer.CheckAdmin(c) {
				return
			}
			c.Next()
			return
		}

		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// Lists are filtered in the business layer (they need count and
			// paging); a single-resource read is answered here so a deep link
			// cannot show a row the list would hide.
			if state != nil && state.ReadEnabled() && h.authorizeRead(c, state) {
				return
			}
			c.Next()
			return
		}

		if claims.IsAdmin {
			c.Next()
			return
		}

		// A Memory is a shared policy object: its owner decides how every
		// subject's entries are embedded, retained and retrieved. Letting any
		// signed-in user create one would let them redirect another agent's
		// memory, so configuration writes stay administrator-only. Reads are
		// already open above, and the entry surface has its own self-service
		// routes.
		if isMemoryPath(c.Request.URL.Path) {
			abortForbidden(c, "只有管理员可以管理记忆库")
			return
		}

		if claims.UserID == "" || claims.UserID == "anonymous" {
			abortForbidden(c, anonymousMessage)
			return
		}

		if h.resources == nil {
			// A handler without a store cannot answer the ownership question.
			// Allowing the write would be the one failure mode this guard
			// exists to prevent, so it refuses instead.
			abortForbidden(c, unavailableMsg)
			return
		}

		// Policy first: a matching Deny must win over ownership, and a matching
		// Allow must be able to grant a non-owner. NoOpinion falls through to
		// the legacy owner rule (additive/dry-run) or is refused for shared
		// resources (authoritative).
		if state != nil && state.WriteEnabled() && h.authorizeWrite(c, state) {
			return
		}

		if !h.enforceOwnership(c, claims) {
			return
		}
		c.Next()
	}
}

// guardedResource is one console resource the policy guard understands.
type guardedResource struct {
	base        string
	kind        string
	ownerScoped bool
	// clusterScoped resources are created without an enclosing namespace, so a
	// create is checked as `write <kind>:*` rather than a namespace write.
	clusterScoped bool
}

// guardedResourceTable is the single mapping from URL to policy kind. It is
// deliberately explicit: deriving the kind from a path segment would turn
// `/v1/agent/templates` into "templates" and miss the policy vocabulary.
var guardedResourceTable = []guardedResource{
	{base: "/v1/agent/agents", kind: "agent", ownerScoped: true},
	{base: "/v1/agent/templates", kind: "agenttemplate"},
	{base: "/v1/agent/agentbindings", kind: "agentbinding"},
	{base: "/v1/agent/clusteragentbindings", kind: "clusteragentbinding", clusterScoped: true},
	{base: "/v1/agent/referencegrants", kind: "referencegrant"},
	{base: "/v1/agent/skills", kind: "skill"},
	{base: "/v1/agent/mcpservers", kind: "mcp"},
	{base: "/v1/agent/knowledgebases", kind: "knowledgebase"},
	{base: "/v1/agent/memories", kind: "memory", ownerScoped: true},
	{base: "/v1/agent/models", kind: "model", ownerScoped: true},
	{base: "/v1/agent/prompts", kind: "prompt"},
	{base: "/v1/agent/customcommands", kind: "customcommand"},
	{base: "/v1/agent/webhooks", kind: "webhook", ownerScoped: true},
	{base: "/v1/agent/scheduledruns", kind: "scheduledrun", ownerScoped: true},
	{base: "/v1/agent/runtimeprofiles", kind: "runtimeprofile", ownerScoped: true},
	{base: "/v1/agent/runtimehostings", kind: "runtimehosting"},
	{base: "/v1/agent/sandboxtemplates", kind: "sandboxtemplate"},
	{base: "/v1/agent/namespaces", kind: "namespace", ownerScoped: true, clusterScoped: true},
}

// writeTarget is the policy check a write maps to.
type writeTarget struct {
	kind        string
	name        string
	ownerScoped bool
}

// writeTargetOf maps a write request to the (kind, name) the policy evaluates.
// It returns false for paths that are not a resource write — action subroutes
// such as `/v1/agent/models/test`, and every resource not in the table.
func writeTargetOf(c *gin.Context) (writeTarget, bool) {
	path := c.Request.URL.Path
	for _, resource := range guardedResourceTable {
		rest, ok := strings.CutPrefix(path, resource.base)
		if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
			continue
		}
		segments := strings.Split(strings.Trim(rest, "/"), "/")
		switch c.Request.Method {
		case http.MethodPost:
			if len(segments) > 0 && segments[0] != "" {
				return writeTarget{}, false
			}
			switch {
			case resource.kind == "namespace":
				// Creating a namespace is a namespace-level write; the new
				// object's name is in the body, which the guard does not parse.
				return writeTarget{kind: "namespace", name: "*", ownerScoped: true}, true
			case resource.clusterScoped:
				return writeTarget{kind: resource.kind, name: "*", ownerScoped: resource.ownerScoped}, true
			default:
				return writeTarget{
					kind:        "namespace",
					name:        requestNamespace(c),
					ownerScoped: resource.ownerScoped,
				}, true
			}
		case http.MethodPut, http.MethodDelete, http.MethodPatch:
			if len(segments) != 1 || segments[0] == "" {
				return writeTarget{}, false
			}
			name, err := url.PathUnescape(segments[0])
			if err != nil {
				name = segments[0]
			}
			return writeTarget{kind: resource.kind, name: name, ownerScoped: resource.ownerScoped}, true
		}
	}
	return writeTarget{}, false
}

// authorizeWrite applies the policy decision for one write. It returns true
// when it has answered the request (allow or refuse) and false when the legacy
// owner rule should still run.
func (h *Handler) authorizeWrite(c *gin.Context, state *authzview.State) bool {
	target, ok := writeTargetOf(c)
	if !ok {
		return false
	}
	decision, reason := state.Decide(c.Request.Context(), authz.VerbWrite, target.kind, target.name)
	switch decision {
	case authz.DecisionAllow:
		c.Next()
		return true
	case authz.DecisionDeny:
		abortForbidden(c, "没有写入该资源的权限："+reason)
		return true
	}

	// No rule spoke.
	switch state.WriteMode {
	case authzview.ModeDryRun:
		slog.InfoContext(c.Request.Context(), "authz: resource guard dry-run",
			"path", c.Request.URL.Path, "kind", target.kind, "name", target.name,
			"decision", "no_opinion")
		return false
	case authzview.ModeAuthoritative:
		if target.ownerScoped {
			// Owner-scoped resources keep the owner rule; the caller may still
			// be refused by enforceOwnership.
			return false
		}
		abortForbidden(c, "没有策略授权写入该资源")
		return true
	default:
		return false
	}
}

// readTarget is the policy check one single-object GET maps to.
type readTarget struct {
	base        string
	kind        string
	name        string
	ownerScoped bool
}

// readTargetOf maps a GET to a single resource, or returns false for a list and
// for sub-resource paths. A deep link is the case list filtering cannot cover:
// it must answer the same question the list answered when it hid the row.
func readTargetOf(c *gin.Context) (readTarget, bool) {
	path := c.Request.URL.Path
	for _, resource := range guardedResourceTable {
		rest, ok := strings.CutPrefix(path, resource.base)
		if !ok || !strings.HasPrefix(rest, "/") {
			continue
		}
		segments := strings.Split(strings.Trim(rest, "/"), "/")
		// Zero segments is the list; more than one is a sub-resource
		// (/effective, /executions, /api-key) whose handler owns its own
		// authorization question.
		if len(segments) != 1 || segments[0] == "" {
			return readTarget{}, false
		}
		name, err := url.PathUnescape(segments[0])
		if err != nil {
			name = segments[0]
		}
		return readTarget{
			base:        resource.base,
			kind:        resource.kind,
			name:        name,
			ownerScoped: resource.ownerScoped,
		}, true
	}
	return readTarget{}, false
}

// authorizeRead hides one resource from a caller the policy denies. It returns
// true when it has answered the request.
//
// A row that does not exist is left to the handler: turning 404 into 403 would
// make a typo look like a permission problem. An unowned (shared) resource is
// evaluated without an owner bypass.
func (h *Handler) authorizeRead(c *gin.Context, state *authzview.State) bool {
	target, ok := readTargetOf(c)
	if !ok || state.Admin {
		return false
	}

	owner := ""
	if target.ownerScoped {
		got, err := h.ownerOf(c.Request.Context(), target.base, target.name)
		switch {
		case errors.Is(err, errResourceNotFound) || errors.Is(err, store.ErrNotFound):
			return false
		case err != nil:
			abortInternal(c, "读取资源归属失败")
			return true
		}
		owner = got
	}

	namespace := ""
	if target.kind != "namespace" {
		namespace = strings.TrimSpace(c.Query("namespace"))
	}
	if state.Visible(c.Request.Context(), target.kind, namespace, target.name, owner) {
		return false
	}
	abortForbidden(c, "没有读取该资源的权限")
	return true
}

// ownerOf resolves a row's owner through the same table the write guard uses.
// A kind without an owner column is shared and returns "".
func (h *Handler) ownerOf(ctx context.Context, base, name string) (string, error) {
	for _, resource := range h.ownedResources() {
		if resource.base == base {
			return resource.owner(ctx, name)
		}
	}
	return "", nil
}

// authzState builds the request-scoped authorization view, or nil when the
// guard is off. The user's group codes ride along so a group grant applies to
// the console's own reads and writes, not only to agent runs.
func (h *Handler) authzState(c *gin.Context, claims *bearer.Claims) *authzview.State {
	if h.authz == nil {
		return nil
	}
	writeMode := h.resourceGuard.WriteMode
	if writeMode == "" {
		writeMode = authzview.ModeOff
	}
	readMode := authzview.ModeOff
	if h.resourceGuard.ReadFilterEnabled {
		// Reads are additive: an operator's explicit Deny hides a row, while a
		// deployment that has written no rule yet keeps its legacy visibility.
		readMode = authzview.ModeAdditive
	}
	if writeMode == authzview.ModeOff && readMode == authzview.ModeOff {
		return nil
	}
	subject := authz.SubjectFromUser(claims.UserID, claims.Name)
	if h.resources != nil {
		if codes, err := h.resources.UserGroups().GroupCodesFor(c.Request.Context(), claims.UserID); err == nil && len(codes) > 0 {
			subject.Extra = map[string]string{"group_codes": strings.Join(codes, ",")}
		} else if err != nil {
			slog.WarnContext(c.Request.Context(),
				"authz: could not resolve user groups for the resource guard; continuing with the user subject only",
				"user_id", claims.UserID, "error", err)
		}
	}
	return authzview.NewState(h.authz, subject, claims.IsAdmin, readMode, writeMode, h.resourceGuard.ReadFilterKinds)
}

// requestNamespace reads the namespace a create targets: the query parameter
// first (the console sends it for scoped pages), then the body's first
// `namespace` string, then the platform default.
func requestNamespace(c *gin.Context) string {
	if ns := strings.TrimSpace(c.Query("namespace")); ns != "" {
		return ns
	}
	if c.Request.Body != nil {
		body, err := io.ReadAll(c.Request.Body)
		if err == nil {
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			var payload map[string]any
			if json.Unmarshal(body, &payload) == nil {
				if ns := findString(payload, "namespace"); ns != "" {
					return ns
				}
			}
		}
	}
	return sharing.DefaultNamespace
}

// findString walks a decoded JSON object looking for key with a string value.
// It is intentionally shallow in meaning: the resource object is wrapped in an
// item key whose name varies, so the guard cannot address it by path.
func findString(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed[key]; ok {
			if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		for _, child := range typed {
			if found := findString(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findString(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

// governancePaths are the resources only an administrator may touch, on every
// method. They are matched by path prefix so subresources are covered too.
var governancePaths = []string{
	"/v1/core/secrets",
	"/v1/agent/authorizationrules",
	// Group membership is an authorization input: allowing a non-administrator
	// to rewrite it would let them grant themselves every rule that names the
	// group.
	"/v1/agent/usergroups",
	// A grant crosses a tenant boundary. Until the platform has a per-namespace
	// grant authorizer, letting only an administrator write it is the safe
	// default: a tenant cannot widen its own reach.
	"/v1/agent/referencegrants",
	// TokenReview and the endpoint description are administrator surfaces: a
	// signed-in user learning whether an arbitrary token is valid is a
	// token-verification oracle. SubjectAccessReview is deliberately not here:
	// the handler narrows a non-administrator to its own identity, so a user may
	// ask "may I ..." but never enumerate somebody else's permissions.
	"/v1/authn/tokenreviews",
	"/v1/authn/webhook-info",
}

// isMemoryPath reports whether path is the Memory configuration surface.
func isMemoryPath(path string) bool {
	const base = "/v1/agent/memories"
	return path == base || strings.HasPrefix(path, base+"/")
}

func isGovernancePath(path string) bool {
	for _, prefix := range governancePaths {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// ownedResource describes one resource whose writes are restricted to its
// owner. base is the collection URL; itemKey is the JSON field that wraps the
// object in a create request.
type ownedResource struct {
	base    string
	itemKey string
	owner   func(ctx context.Context, name string) (string, error)
}

// ownedResources is the owner-scoping table. It lists exactly the resources
// whose model carries an `owner` column; the rest are shared.
//
// Every getter returns the same two answers: an owner string, or
// errResourceNotFound. A backend failure is passed through so the request fails
// closed rather than guessing.
func (h *Handler) ownedResources() []ownedResource {
	return []ownedResource{
		{
			base:    "/v1/agent/agents",
			itemKey: "agent",
			owner: func(ctx context.Context, name string) (string, error) {
				row, err := h.resources.Agents().Get(ctx, where.F("name", name))
				if err != nil {
					return "", err
				}
				if row == nil {
					return "", errResourceNotFound
				}
				return row.Owner, nil
			},
		},
		{
			base:    "/v1/agent/memories",
			itemKey: "memory",
			owner: func(ctx context.Context, name string) (string, error) {
				row, err := h.resources.Memories().Get(ctx, where.F("name", name))
				if err != nil {
					return "", err
				}
				if row == nil {
					return "", errResourceNotFound
				}
				return row.Owner, nil
			},
		},
		{
			base:    "/v1/agent/models",
			itemKey: "model",
			owner: func(ctx context.Context, name string) (string, error) {
				row, err := h.resources.Models().Get(ctx, where.F("name", name))
				if err != nil {
					return "", err
				}
				if row == nil {
					return "", errResourceNotFound
				}
				return row.Owner, nil
			},
		},
		{
			base:    "/v1/agent/namespaces",
			itemKey: "namespace",
			owner: func(ctx context.Context, name string) (string, error) {
				row, err := h.resources.Namespaces().Get(ctx, where.F("name", name))
				if err != nil {
					return "", err
				}
				if row == nil {
					return "", errResourceNotFound
				}
				return row.Owner, nil
			},
		},
		{
			base:    "/v1/agent/runtimeprofiles",
			itemKey: "runtimeProfile",
			owner: func(ctx context.Context, name string) (string, error) {
				row, err := h.resources.RuntimeProfiles().Get(ctx, where.F("name", name))
				if err != nil {
					return "", err
				}
				if row == nil {
					return "", errResourceNotFound
				}
				return row.Owner, nil
			},
		},
		{
			base:    "/v1/agent/scheduledruns",
			itemKey: "scheduledRun",
			owner: func(ctx context.Context, name string) (string, error) {
				row, err := h.resources.ScheduledRuns().Get(ctx, where.F("name", name))
				if err != nil {
					return "", err
				}
				if row == nil {
					return "", errResourceNotFound
				}
				return row.Owner, nil
			},
		},
		{
			base:    "/v1/agent/webhooks",
			itemKey: "webhook",
			owner: func(ctx context.Context, name string) (string, error) {
				row, err := h.resources.Webhooks().Get(ctx, where.F("name", name))
				if err != nil {
					return "", err
				}
				if row == nil {
					return "", errResourceNotFound
				}
				return row.Owner, nil
			},
		},
	}
}

// enforceOwnership applies the owner rule to one request. It returns true when
// the request may proceed, and false after writing the refusal.
func (h *Handler) enforceOwnership(c *gin.Context, claims *bearer.Claims) bool {
	path := c.Request.URL.Path

	for _, res := range h.ownedResources() {
		rest, ok := strings.CutPrefix(path, res.base)
		// No match, or a sibling path that merely shares the prefix (e.g.
		// /v1/agent/agents2).
		if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
			continue
		}

		// The collection itself: a create. Stamp the caller as the owner so the
		// rule does not rely on the client sending the right value.
		if rest == "" || rest == "/" {
			if err := stampOwner(c, res.itemKey, callerIdentity(claims)); err != nil {
				abortInternal(c, "无法读取请求体")
				return false
			}
			return true
		}

		name, _, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
		if name == "" {
			return true
		}

		owner, err := res.owner(c.Request.Context(), name)
		if errors.Is(err, errResourceNotFound) || errors.Is(err, store.ErrNotFound) {
			// No such object. The handler answers 404; the guard must not turn
			// "not found" into "forbidden", which would be indistinguishable
			// from a permission problem. It also keeps action paths whose last
			// segment looks like a name (POST /v1/agent/models/test) working.
			return true
		}
		if err != nil {
			abortInternal(c, "读取资源归属失败")
			return false
		}

		// An ownerless row belongs to the platform (or predates ownership).
		// Only an administrator may change it.
		if owner == "" || !isOwner(claims, owner) {
			abortForbidden(c, ownerlessMessage)
			return false
		}
		return true
	}

	return true
}

// callerIdentity is the owner value stamped on a created resource. It matches
// the console's creator field (user ID first, display name as fallback) so the
// value the client sends and the value the server stores agree.
func callerIdentity(claims *bearer.Claims) string {
	return cmp.Or(claims.UserID, claims.Name)
}

// isOwner accepts either identity the token carries. Existing rows store the
// display name (Lark user names predate account IDs), while new rows store the
// user ID; a caller must be recognized under both.
func isOwner(claims *bearer.Claims, owner string) bool {
	if owner == "" {
		return false
	}
	return owner == claims.UserID || (claims.Name != "" && owner == claims.Name)
}

// stampOwner rewrites a create body so the wrapped object's `owner` is the
// caller. It leaves every other byte-level detail to the handler: a body it
// cannot parse is passed through untouched, so the handler reports its own
// validation error instead of the guard inventing a different one.
func stampOwner(c *gin.Context, itemKey, owner string) error {
	if c.Request.Body == nil {
		return nil
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return err
	}
	// Restore first: every early return below leaves the request readable.
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	if len(body) == 0 {
		return nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	wrapped, ok := payload[itemKey]
	if !ok {
		return nil
	}
	var item map[string]any
	if err := json.Unmarshal(wrapped, &item); err != nil || item == nil {
		// Not an object (null, a scalar, …). Leave the body alone so the
		// handler's validation produces the error.
		return nil
	}
	item["owner"] = owner
	encodedItem, err := json.Marshal(item)
	if err != nil {
		return err
	}
	payload[itemKey] = encodedItem
	out, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(out))
	c.Request.ContentLength = int64(len(out))
	return nil
}

// abortForbidden writes the same body shape bearer.CheckAdmin uses, so the
// console renders one message for every "you may not" answer.
func abortForbidden(c *gin.Context, message string) {
	core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("%s", message))
	c.Abort()
}

func abortInternal(c *gin.Context, message string) {
	core.WriteResponse(c, nil, errno.ErrInternal.WithMessage("%s", message))
	c.Abort()
}
