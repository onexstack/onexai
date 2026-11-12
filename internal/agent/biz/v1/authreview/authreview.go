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

// Package authreview implements the Kubernetes access-review protocols the
// platform exposes: authentication.k8s.io/v1 TokenReview and
// authorization.k8s.io/v1 SubjectAccessReview.
//
// It is the single bridge between the Kubernetes vocabulary and the platform's
// own one. TokenReview is answered by the same JWT parser the Gin middleware
// uses (see bearer.ParseToken), and SubjectAccessReview is answered by the same
// authorization Coordinator the run path uses. The alternative — a second
// parser or a second policy evaluation for these endpoints — would let the
// review answer differ from what the runtime actually enforces.
package authreview

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"

	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

const (
	// TokenReviewPath is the Kubernetes-native path a webhook caller uses.
	TokenReviewPath = "/apis/authentication.k8s.io/v1/tokenreviews"
	// SubjectAccessReviewPath is the Kubernetes-native path a webhook caller uses.
	SubjectAccessReviewPath = "/apis/authorization.k8s.io/v1/subjectaccessreviews"
)

// Config is what the review business logic needs beyond the store and the
// authorization coordinator.
type Config struct {
	// JWTSecret validates the platform's bearer tokens. It is the same secret
	// the Gin middleware uses.
	JWTSecret string
	// WebhookTokenConfigured reports whether the native webhook endpoints have a
	// caller credential. The review logic itself never authenticates the caller;
	// the HTTP middleware does. This flag is only reported by Info.
	WebhookTokenConfigured bool
	// TokenAuthenticator is the same Kubernetes authenticator chain the HTTP
	// middleware uses. TokenReview tries it first, so a token the platform
	// accepts over HTTP is also accepted by the review endpoint; a nil chain
	// falls back to the platform HS256 parser.
	TokenAuthenticator authenticator.Request
	// AuthenticationConfig and AuthorizationConfig are the declarative files the
	// process was started with, if any. They are reported, not parsed: the
	// console shows which file is in force so an administrator knows whether an
	// edit there needs a restart (the agent loads them at start-up).
	AuthenticationConfig string
	AuthorizationConfig  string
}

// Info is the read-only description the console shows.
type Info struct {
	CallerAuthConfigured    bool   `json:"callerAuthConfigured"`
	TokenReviewPath         string `json:"tokenReviewPath"`
	SubjectAccessReviewPath string `json:"subjectAccessReviewPath"`
	// AuthenticationConfig and AuthorizationConfig are the declarative files in
	// force, empty when the deployment uses flags/legacy endpoints only.
	AuthenticationConfig string `json:"authenticationConfig,omitempty"`
	AuthorizationConfig  string `json:"authorizationConfig,omitempty"`
}

// Biz is the access-review business logic.
type Biz interface {
	TokenReview(ctx context.Context, req *authenticationv1.TokenReview) *authenticationv1.TokenReview
	SubjectAccessReview(ctx context.Context, req *authorizationv1.SubjectAccessReview) *authorizationv1.SubjectAccessReview
	// SelfSubject returns the identity a signed-in user presents to a review.
	// The console's non-administrator path uses it so a caller cannot forge a
	// group it does not belong to.
	SelfSubject(ctx context.Context, claims *bearer.Claims) (userID string, groups []string)
	Info() Info
}

type biz struct {
	cfg        Config
	resources  agentstore.AIStore
	authorizer *authz.Coordinator
}

var _ Biz = (*biz)(nil)

// New builds the review business logic. A nil store or coordinator degrades to
// "no group expansion" / "authorization disabled", which is the same behaviour
// the rest of the service has when those dependencies are absent.
func New(cfg Config, resources agentstore.AIStore, coordinator *authz.Coordinator) Biz {
	return &biz{cfg: cfg, resources: resources, authorizer: coordinator}
}

// Info implements Biz.
func (b *biz) Info() Info {
	return Info{
		CallerAuthConfigured:    b.cfg.WebhookTokenConfigured,
		TokenReviewPath:         TokenReviewPath,
		SubjectAccessReviewPath: SubjectAccessReviewPath,
		AuthenticationConfig:    b.cfg.AuthenticationConfig,
		AuthorizationConfig:     b.cfg.AuthorizationConfig,
	}
}

// SelfSubject implements Biz.
func (b *biz) SelfSubject(ctx context.Context, claims *bearer.Claims) (string, []string) {
	if claims == nil {
		return "", nil
	}
	return claims.UserID, b.groupsFor(ctx, claims)
}

// TokenReview implements Biz.
//
// A failed authentication is an answer, not a server error: the Kubernetes
// protocol carries the reason in status.error and expects HTTP 2xx.
func (b *biz) TokenReview(ctx context.Context, req *authenticationv1.TokenReview) *authenticationv1.TokenReview {
	out := &authenticationv1.TokenReview{}
	if req != nil {
		out = req.DeepCopy()
		// The caller already holds the token; echoing it puts a credential into
		// response logs and proxies for no benefit.
		out.Spec.Token = ""
	}
	out.Status = authenticationv1.TokenReviewStatus{Audiences: append([]string(nil), specAudiences(req)...)}

	token := ""
	if req != nil {
		token = strings.TrimSpace(req.Spec.Token)
	}
	if token == "" {
		out.Status.Error = "token is required"
		return out
	}

	if b.cfg.TokenAuthenticator != nil {
		if info, ok := b.authenticateWithKubernetes(ctx, token); ok {
			out.Status.Authenticated = true
			out.Status.User = userInfoFromKubernetes(info)
			return out
		}
	}

	claims, err := bearer.ParseToken(token, b.cfg.JWTSecret)
	if err != nil {
		out.Status.Error = "invalid or expired token"
		return out
	}
	if claims.UserID == "" {
		out.Status.Error = "token carries no user identity"
		return out
	}

	out.Status.Authenticated = true
	out.Status.User = authenticationv1.UserInfo{
		Username: claims.UserID,
		UID:      claims.UserID,
		Groups:   b.groupsFor(ctx, claims),
		Extra:    extraFor(claims),
	}
	return out
}

// authenticateWithKubernetes asks the same authenticator chain the HTTP
// middleware uses. The token is presented as a Bearer header because that is
// what the upstream OIDC authenticator reads; no real request leaves the
// process.
func (b *biz) authenticateWithKubernetes(ctx context.Context, token string) (user.Info, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, ok, err := b.cfg.TokenAuthenticator.AuthenticateRequest(req)
	if err != nil || !ok || resp == nil || resp.User == nil {
		return nil, false
	}
	return resp.User, true
}

func userInfoFromKubernetes(info user.Info) authenticationv1.UserInfo {
	out := authenticationv1.UserInfo{
		Username: info.GetName(),
		UID:      info.GetUID(),
		Groups:   append([]string(nil), info.GetGroups()...),
		Extra:    map[string]authenticationv1.ExtraValue{},
	}
	for key, values := range info.GetExtra() {
		out.Extra[key] = authenticationv1.ExtraValue(append([]string(nil), values...))
	}
	return out
}

// SubjectAccessReview implements Biz.
func (b *biz) SubjectAccessReview(
	ctx context.Context, req *authorizationv1.SubjectAccessReview,
) *authorizationv1.SubjectAccessReview {
	out := &authorizationv1.SubjectAccessReview{}
	if req != nil {
		out = req.DeepCopy()
	}
	out.Status = authorizationv1.SubjectAccessReviewStatus{}

	if req == nil {
		out.Status.Denied = true
		out.Status.Reason = "request is required"
		return out
	}
	if strings.TrimSpace(req.Spec.User) == "" {
		out.Status.Denied = true
		out.Status.Reason = "user is required"
		return out
	}
	if req.Spec.ResourceAttributes == nil {
		// The platform's authorization model has no path-based dimensions; an
		// answer of "allow" for a question the policy was never asked is the one
		// outcome a review endpoint must not produce.
		out.Status.Denied = true
		out.Status.Reason = "non-resource authorization is not supported by this authorizer"
		return out
	}

	verb, rawVerb, ok := reviewVerb(req.Spec.ResourceAttributes)
	if !ok {
		out.Status.Denied = true
		out.Status.Reason = fmt.Sprintf("unsupported verb %q", rawVerb)
		return out
	}

	extra := flattenExtra(req.Spec.Extra)
	target := targetFor(req.Spec.ResourceAttributes, extra)
	subjects := subjectsFor(req.Spec.User, req.Spec.Groups, extra)

	decision, reason, err := b.authorizer.AuthorizeScoped(
		ctx, subjects, verb, target.kind, target.name, target.agentNames, target.attributes, authz.DecisionDeny,
	)
	if err != nil {
		out.Status.EvaluationError = err.Error()
		return out
	}

	switch decision {
	case authz.DecisionAllow:
		out.Status.Allowed = true
	case authz.DecisionDeny:
		out.Status.Denied = true
	default:
		// The run path fails closed on NoOpinion. Returning a silent "neither
		// allowed nor denied" here would let a caller that only reads Allowed
		// treat the absence of a rule as a grant.
		out.Status.Denied = true
		if reason == "" {
			reason = "no opinion (fail-closed)"
		} else {
			reason = "no opinion (fail-closed): " + reason
		}
	}
	out.Status.Reason = reason
	return out
}

func specAudiences(req *authenticationv1.TokenReview) []string {
	if req == nil {
		return nil
	}
	return req.Spec.Audiences
}

// groupsFor is the one place a platform identity becomes Kubernetes groups.
// system:authenticated is always present; the role groups are the vocabulary a
// Kubernetes RBAC or webhook authorizer can key on.
func (b *biz) groupsFor(ctx context.Context, claims *bearer.Claims) []string {
	groups := []string{"system:authenticated"}
	if claims.IsAdmin {
		groups = append(groups, "system:masters", "onexai:admin")
	}
	if claims.IsSRE {
		groups = append(groups, "onexai:sre")
	}
	if b.resources != nil && claims.UserID != "" {
		codes, err := b.resources.UserGroups().GroupCodesFor(ctx, claims.UserID)
		if err != nil {
			// The policy is fail-closed, so a missing group can only withhold a
			// grant. Failing the review instead would turn a transient database
			// problem into a 500 on an authentication API.
			slog.WarnContext(ctx, "authreview: could not resolve user groups; continuing without them",
				"user_id", claims.UserID, "error", err)
		} else {
			groups = append(groups, codes...)
		}
	}
	return dedupeSorted(groups)
}

// extraFor exposes the claims a policy or an operator may need beyond the
// username and groups. Keys are namespaced so they cannot collide with a
// third-party claim.
func extraFor(claims *bearer.Claims) map[string]authenticationv1.ExtraValue {
	extra := make(map[string]authenticationv1.ExtraValue, 6)
	add := func(key, value string) {
		if value == "" {
			return
		}
		extra[key] = authenticationv1.ExtraValue{value}
	}
	add("onexai.io/name", claims.Name)
	add("onexai.io/provider", claims.Provider)
	add("onexai.io/account-role", claims.AccountRole)
	add("onexai.io/open-id", claims.OpenID)
	if claims.ExternalAdmin {
		extra["onexai.io/external-admin"] = authenticationv1.ExtraValue{"true"}
	}
	if claims.ExternalSRE {
		extra["onexai.io/external-sre"] = authenticationv1.ExtraValue{"true"}
	}
	return extra
}

// reviewVerb maps Kubernetes verbs to the platform's two-verb model.
func reviewVerb(attrs *authorizationv1.ResourceAttributes) (authz.Verb, string, bool) {
	raw := strings.ToLower(strings.TrimSpace(attrs.Verb))
	switch raw {
	case "get", "list", "watch":
		return authz.VerbRead, raw, true
	case "create", "update", "patch", "delete", "deletecollection":
		return authz.VerbWrite, raw, true
	default:
		return "", raw, false
	}
}

// resourceTarget is the platform resource a SubjectAccessReview addresses.
type resourceTarget struct {
	kind       authz.ResourceKind
	name       string
	agentNames []string
	attributes map[string]string
}

func targetFor(attrs *authorizationv1.ResourceAttributes, extra map[string]string) resourceTarget {
	kind := sarResourceKind(attrs.Resource)
	name := attrs.Name

	if v := firstExtra(extra, "onexai.io/resourceKind", "onexai.io/resource-kind"); v != "" {
		kind = v
	}
	if v := firstExtra(extra, "onexai.io/resourceName", "onexai.io/resource-name"); v != "" {
		name = v
	}

	attributes := make(map[string]string, len(extra)+3)
	for k, v := range extra {
		if strings.HasPrefix(k, "onexai.io/") {
			continue
		}
		attributes[k] = v
	}
	if attrs.Namespace != "" {
		attributes["namespace"] = attrs.Namespace
	}
	if attrs.Subresource != "" {
		attributes["subresource"] = attrs.Subresource
	}
	if attrs.Group != "" {
		attributes["group"] = attrs.Group
	}
	if attrs.Version != "" {
		attributes["version"] = attrs.Version
	}

	var agentNames []string
	if kind == string(authz.ResourceKindAgent) && name != "" {
		agentNames = []string{name}
	}
	if v := firstExtra(extra, "onexai.io/agent"); v != "" {
		agentNames = []string{v}
	}
	if v := firstExtra(extra, "onexai.io/agent-names"); v != "" {
		agentNames = splitList(v)
	}

	return resourceTarget{kind: authz.ResourceKind(kind), name: name, agentNames: agentNames, attributes: attributes}
}

// subjectsFor turns one Kubernetes user + its group list into the single user
// subject the platform's policy evaluates. The groups travel in Extra as the
// comma-separated group_codes list, which is the same shape the run path's
// OPA input uses — a rule written for a group therefore applies identically to a
// run and to a review.
func subjectsFor(user string, groups []string, extra map[string]string) []authz.Subject {
	identity := copyExtra(extra)
	if rest, ok := strings.CutPrefix(user, "system:serviceaccount:"); ok {
		if _, name, found := strings.Cut(rest, ":"); found && name != "" {
			return []authz.Subject{{
				Kind:        authz.SubjectKindServiceAccount,
				ServiceName: name,
				Extra:       identity,
			}}
		}
	}

	all := append([]string(nil), groups...)
	if v := firstExtra(extra, "onexai.io/group-codes", "onexai.io/groupCodes"); v != "" {
		all = append(all, splitList(v)...)
	}
	if len(all) > 0 {
		identity["group_codes"] = strings.Join(dedupeSorted(all), ",")
	}

	return []authz.Subject{{
		Kind:     authz.SubjectKindUser,
		UserID:   user,
		UserName: identity["onexai.io/name"],
		Extra:    identity,
	}}
}

// sarResourceKinds maps the Kubernetes spelling of a platform object to the
// policy kind. Each console resource appears in both forms: a webhook caller
// sends what kubectl would (`agents`, `mcpservers`), while the console's policy
// vocabulary uses the bare kind (`agent`, `mcp`). Both must land on the same
// kind, otherwise a rule written in the console would silently never match a
// review issued by an external apiserver.
//
// The list mirrors internal/agent/handler's guardedResourceTable plus the
// resource kinds the console's policy editor offers. A value that is not here
// is passed through verbatim, so a policy naming a new kind still works; only
// the spellings below are guaranteed to line up with the console.
var sarResourceKinds = map[string]string{
	"agent":                string(authz.ResourceKindAgent),
	"agents":               string(authz.ResourceKindAgent),
	"agenttemplate":        "agenttemplate",
	"agenttemplates":       "agenttemplate",
	"agentbinding":         "agentbinding",
	"agentbindings":        "agentbinding",
	"clusteragentbinding":  "clusteragentbinding",
	"clusteragentbindings": "clusteragentbinding",
	"referencegrant":       "referencegrant",
	"referencegrants":      "referencegrant",
	"skill":                string(authz.ResourceKindSkill),
	"skills":               string(authz.ResourceKindSkill),
	"mcp":                  string(authz.ResourceKindMCP),
	"mcps":                 string(authz.ResourceKindMCP),
	"mcpserver":            string(authz.ResourceKindMCP),
	"mcpservers":           string(authz.ResourceKindMCP),
	"tool":                 string(authz.ResourceKindTool),
	"tools":                string(authz.ResourceKindTool),
	"knowledgebase":        string(authz.ResourceKindKnowledgeBase),
	"knowledgebases":       string(authz.ResourceKindKnowledgeBase),
	"memory":               "memory",
	"memories":             "memory",
	"prompt":               "prompt",
	"prompts":              "prompt",
	"model":                "model",
	"models":               "model",
	"customcommand":        "customcommand",
	"customcommands":       "customcommand",
	"scheduledrun":         "scheduledrun",
	"scheduledruns":        "scheduledrun",
	"webhook":              "webhook",
	"webhooks":             "webhook",
	"runtimeprofile":       "runtimeprofile",
	"runtimeprofiles":      "runtimeprofile",
	"runtimehosting":       "runtimehosting",
	"runtimehostings":      "runtimehosting",
	"sandboxtemplate":      "sandboxtemplate",
	"sandboxtemplates":     "sandboxtemplate",
	"namespace":            string(authz.ResourceKindNamespace),
	"namespaces":           string(authz.ResourceKindNamespace),
	"k8snamespace":         string(authz.ResourceKindK8sNamespace),
	"k8snamespaces":        string(authz.ResourceKindK8sNamespace),
	"k8s-namespace":        string(authz.ResourceKindK8sNamespace),
	"k8s-namespaces":       string(authz.ResourceKindK8sNamespace),
	"role":                 string(authz.ResourceKindRole),
	"roles":                string(authz.ResourceKindRole),
	"service":              string(authz.ResourceKindService),
	"services":             string(authz.ResourceKindService),
	"cluster":              string(authz.ResourceKindCluster),
	"clusters":             string(authz.ResourceKindCluster),
	"authorizationrule":    "authorizationrule",
	"authorizationrules":   "authorizationrule",
	"usergroup":            "usergroup",
	"usergroups":           "usergroup",
}

func sarResourceKind(resource string) string {
	normalized := strings.ToLower(strings.TrimSpace(resource))
	if kind, ok := sarResourceKinds[normalized]; ok {
		return kind
	}
	// The authz package deliberately accepts any string as a kind, so an
	// unknown resource is not an error: a policy that names it can match it.
	return normalized
}

func flattenExtra(in map[string]authorizationv1.ExtraValue) map[string]string {
	out := make(map[string]string, len(in))
	for key, values := range in {
		if len(values) == 0 {
			continue
		}
		out[key] = strings.Join(values, ",")
	}
	return out
}

func copyExtra(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func firstExtra(extra map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := extra[key]; v != "" {
			return v
		}
	}
	return ""
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func dedupeSorted(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}
