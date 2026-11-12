package handler

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/onexstack/onexai/internal/agent/biz"
	"github.com/onexstack/onexai/internal/agent/biz/v1/authreview"
	"github.com/onexstack/onexai/internal/agent/biz/v1/extensions"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/inputaudit"
	"github.com/onexstack/onexai/internal/agent/pkg/interactionlog"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/agent/pkg/validation"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// Screener decides whether a user's message may reach a model. It is an alias
// of the audit package's own type rather than a second named type with the same
// shape: two identical func types would force a conversion at every boundary
// and give a reader two names to reconcile. A nil Screener simply does not
// screen.
type Screener = inputaudit.Screener

// Handler aggregates all route handlers.
type Handler struct {
	biz biz.IBiz
	// screen is the inbound audit, or nil when it is not configured.
	screen Screener
	// interactions records the turns this surface blocked, which the agent
	// service never sees and therefore cannot record itself.
	interactions *interactionlog.Writer
	// a2a holds the Agent-to-Agent transports. Always set: the routes exist in
	// every deployment (see ProvideA2AHandlers).
	a2a *A2AHandlers
	// val checks resource-management requests. NewHandler always provides one.
	val *validation.Validator
	// resources is the read-only resource store the validator holds. It is kept
	// so the option endpoints (namespaces) can reach the capabilities the store
	// offers beyond the five CRUD methods, by type assertion.
	resources store.AIStore
	// authz is the process-wide authorization entry point, or nil when
	// authorization is disabled. Its registry names are the values an Agent's
	// authzBackends may reference, which is what GET /v1/authz/backends reports.
	authz *authz.Coordinator
	// authReview answers the Kubernetes TokenReview/SubjectAccessReview
	// protocols, or nil when the review surface is not wired.
	authReview authreview.Biz
	// extensions reads and writes the control plane's own extension objects
	// (admission webhook configurations, CEL policies, APIService). Nil disables
	// the extension pages.
	extensions extensions.Biz
	// media serves the objects a local store holds, or nil when the driver is
	// an object store or nothing is configured.
	media MediaHandler
	// outputCaps resolves the per-surface output ceiling a run started here is
	// capped at, or nil when the deployment declares no surfaces — in which case
	// llm.max-tokens is the only ceiling and the run is capped the same wherever
	// it came from. See outputcap.go.
	outputCaps *OutputCaps
	// decisionCallers, when non-empty, is the set of service subjects allowed to
	// ask /v1/authz/decisions. Empty allows any signed caller.
	decisionCallers map[string]struct{}
	// resourceGuard controls whether policy decisions gate console CRUD.
	resourceGuard ResourceGuard
}

// ResourceGuard is the console resource guard's runtime configuration.
//
// The zero value means "off": a Handler built without options behaves exactly
// as it did before the guard existed, which is what tests and embedded uses
// get.
type ResourceGuard struct {
	WriteMode         authzview.Mode
	ReadFilterEnabled bool
	ReadFilterKinds   map[string]struct{}
}

// ReadFilterEnabledFor reports whether list rows of kind are filtered.
func (g ResourceGuard) ReadFilterEnabledFor(kind string) bool {
	if !g.ReadFilterEnabled {
		return false
	}
	if len(g.ReadFilterKinds) == 0 {
		return true
	}
	_, ok := g.ReadFilterKinds[kind]
	return ok
}

// Registrar defines a function signature for registering HTTP routes.
type Registrar func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc)

var registrars []Registrar

// Deps are the handler layer's dependencies. Everything but Biz may be nil,
// and each nil disables the capability it belongs to rather than failing: a
// deployment chooses which surfaces it exposes.
type Deps struct {
	// Biz is the agent service. Required.
	Biz biz.IBiz
	// Resources backs the validator's read-only existence checks. Business
	// reads go through Biz.
	Resources store.AIStore
	// Screen is the inbound audit.
	Screen Screener
	// Interactions records whether a blocked turn left a trace.
	Interactions *interactionlog.Writer
	// A2A is the Agent-to-Agent transport set.
	A2A *A2AHandlers
	// Validator checks the resource-management requests. Nil is replaced with a
	// default instance by NewHandler.
	Validator *validation.Validator
	// Authz is the authorization coordinator, or nil when authorization is
	// disabled. Only its registered backend names are served.
	Authz *authz.Coordinator
	// AuthReview answers the Kubernetes TokenReview/SubjectAccessReview
	// protocols. Nil disables the review endpoints.
	AuthReview authreview.Biz
	// Extensions reads and writes the control plane's own extension objects.
	// Nil disables the extension pages.
	Extensions extensions.Biz
	// Media serves objects this service stores itself, when the configured
	// driver keeps them on this machine. It is nil for an object store, whose
	// provider serves its own bytes, and for a deployment with no store at all.
	Media MediaHandler
	// OutputCaps is the per-surface output ceiling. Nil means every caller is
	// capped by llm.max-tokens alone.
	OutputCaps *OutputCaps
	// DecisionCallers, when non-empty, is the set of service subjects allowed to
	// ask /v1/authz/decisions. Empty allows any signed caller.
	DecisionCallers map[string]struct{}
	// ResourceGuard applies policy decisions to console CRUD. The zero value
	// disables it.
	ResourceGuard ResourceGuard
}

// NewHandler creates a new instance of Handler.
func NewHandler(deps Deps) *Handler {
	// The validator is part of the request contract, so a partly constructed
	// handler still gets one instead of silently skipping validation.
	if deps.Validator == nil {
		deps.Validator = validation.New(deps.Resources)
	}
	return &Handler{
		biz:             deps.Biz,
		screen:          deps.Screen,
		interactions:    deps.Interactions,
		a2a:             deps.A2A,
		val:             deps.Validator,
		resources:       deps.Resources,
		authz:           deps.Authz,
		authReview:      deps.AuthReview,
		extensions:      deps.Extensions,
		media:           deps.Media,
		outputCaps:      deps.OutputCaps,
		decisionCallers: deps.DecisionCallers,
		resourceGuard:   deps.ResourceGuard,
	}
}

// Sources recorded on an audit row, naming the surface a turn arrived on.
const (
	// SourceChat is the streaming agent surface used by the chat service.
	SourceChat = "chat"
	// SourceAPI is the OpenAI-compatible endpoint.
	SourceAPI = "api"
	// SourceConsole is the management surface's debug run. It is its own value
	// rather than one of the two above because it is the only surface where the
	// person who started the run is not the identity the run executes as, and
	// the audit row records the former.
	SourceConsole = "console"
)

// BlockedInput is what a screening refusal needs in order to be recorded: which
// surface it arrived on and who sent it.
type BlockedInput struct {
	Source     string
	SessionKey string
	SenderID   string
	SenderName string
}

// blockedByAudit screens text and, when it is refused, records the blocked turn
// and reports the reason to the caller. It returns true when the caller must
// stop, having already written the response.
//
// The response differs per surface, so it is the caller's to write; only the
// verdict and the audit row live here.
func (h *Handler) blockedByAudit(c *gin.Context, entry BlockedInput, text string) (inputaudit.Result, bool) {
	if h == nil {
		return inputaudit.Result{}, false
	}
	return screenInput(c.Request.Context(), h.screen, h.interactions, entry, text)
}

// screenInput is the gin-free core of blockedByAudit: the A2A surface reaches
// it through the adapter's Guard port, which has no *gin.Context to offer.
//
// It is a free function rather than a Handler method so the two surfaces cannot
// drift: one screener, one metrics counter, one audit row per refusal, whichever
// surface the turn arrived on.
func screenInput(
	ctx context.Context, screen Screener, interactions *interactionlog.Writer, entry BlockedInput, text string,
) (inputaudit.Result, bool) {
	if screen == nil {
		return inputaudit.Result{}, false
	}
	verdict := screen(ctx, text)
	if !verdict.Blocked {
		return inputaudit.Result{}, false
	}
	metrics.M.ObserveBlockedInput(verdict.Category)
	// No AgentNames, because the screen runs before a selection is assembled —
	// which is the point of running it here: a refused message should cost
	// nothing, so there is nothing yet to name.
	//
	// The empty column is ambiguous on its own (a turn that pinned no Agent
	// leaves it empty too), and the row's Status is what tells the two apart.
	// Filling it here would be the worse error: it would attribute a refused
	// message to an Agent that never saw it.
	interactions.Write(ctx, interactionlog.Entry{
		Source:       entry.Source,
		SessionKey:   entry.SessionKey,
		SenderID:     entry.SenderID,
		SenderName:   entry.SenderName,
		UserInput:    text,
		Status:       interactionlog.StatusBlocked,
		ErrorMessage: verdict.Category + ": " + verdict.Reason,
	})
	return verdict, true
}

// SecretKey resolves a secret id to its key, for the AK/SK middleware's
// signature check. It reports false for an unknown id rather than an empty key:
// an empty key would still verify a signature computed with it, which is the
// one outcome a secret lookup must never produce.
func (h *Handler) SecretKey(secretID string) (string, bool) {
	if h == nil || h.biz == nil || secretID == "" {
		return "", false
	}
	return h.biz.SecretV1().Key(context.Background(), secretID)
}

// Register adds a new REST route registrar to the global registry.
func Register(r Registrar) {
	registrars = append(registrars, r)
}

// ApplyTo applies the registered REST API registrars to the provided Gin router group.
func (h *Handler) ApplyTo(engine *gin.RouterGroup, mws ...gin.HandlerFunc) {
	for _, r := range registrars {
		r(engine, h, mws...)
	}

	slog.Info("rest api routes installed", "count", len(registrars))
}

// adminRegistrars is the second registry, for the browser-facing management
// surface. It is separate from `registrars` and not a flag on it, because the
// two sets are mounted on groups with different authentication: ApplyTo mounts
// everything on the signed /v1 group, and reusing it for the management group
// would re-register the service-to-service face under /v1/admin as well.
//
// The split is therefore load-bearing: a route belongs to exactly one of the two
// lists, and which list it is in is what decides who may call it. See admin.go
// for which routes are mounted here and why they are the same handlers as the
// signed face.
var adminRegistrars []Registrar

// RegisterAdmin adds a registrar to the management-surface registry. The caller
// must be an administrator: the group ApplyAdminTo receives carries the token
// check and the role check, and this registry assumes both.
func RegisterAdmin(r Registrar) {
	adminRegistrars = append(adminRegistrars, r)
}

// ApplyAdminTo applies the management-surface registrars to the provided Gin
// router group. The group is expected to be already authenticated and
// admin-gated; this method adds no protection of its own, which is why it is
// only ever called with the /v1/admin group (see httpserver.go).
func (h *Handler) ApplyAdminTo(engine *gin.RouterGroup, mws ...gin.HandlerFunc) {
	for _, r := range adminRegistrars {
		r(engine, h, mws...)
	}

	slog.Info("admin api routes installed", "count", len(adminRegistrars))
}

// selfServiceRegistrars is the fourth registry, for routes any signed-in user
// may call about their own account.
//
// The declarative registry cannot serve it: its bearer branch requires the
// administrator role, and a personal access key belongs to whoever created it,
// not only to administrators. This group therefore authenticates a bearer token
// and adds no role check — each handler narrows its data to claims.UserID, so
// "somebody else's key" is not a route that exists.
var selfServiceRegistrars []Registrar

// RegisterSelfService adds a registrar to the signed-in-user registry.
func RegisterSelfService(r Registrar) {
	selfServiceRegistrars = append(selfServiceRegistrars, r)
}

// ApplySelfServiceTo applies the self-service registrars to the provided Gin
// router group. The group is expected to be already authenticated; this method
// adds no protection of its own.
func (h *Handler) ApplySelfServiceTo(engine *gin.RouterGroup, mws ...gin.HandlerFunc) {
	for _, r := range selfServiceRegistrars {
		r(engine, h, mws...)
	}

	slog.Info("self-service api routes installed", "count", len(selfServiceRegistrars))
}

// unifiedRegistrars is the third registry, for the declarative resource surface.
// The routes in it are mounted once on /v1 behind a middleware that accepts
// either an AK/SK signed service call or an administrator's bearer token, so a
// browser and a service reach the same URL.
//
// It is separate from the other two because neither of their groups can accept
// both credential types, and duplicating the routes under /v1/admin is exactly
// the split this face exists to remove.
var unifiedRegistrars []Registrar

// RegisterUnified adds a registrar to the either-credential resource registry.
func RegisterUnified(r Registrar) {
	unifiedRegistrars = append(unifiedRegistrars, r)
}

// ApplyUnifiedTo applies the either-credential registrars to the provided Gin
// router group. The group is expected to carry UnifiedAuth; this method adds no
// protection of its own.
func (h *Handler) ApplyUnifiedTo(engine *gin.RouterGroup, mws ...gin.HandlerFunc) {
	for _, r := range unifiedRegistrars {
		r(engine, h, mws...)
	}

	slog.Info("unified api routes installed", "count", len(unifiedRegistrars))
}
