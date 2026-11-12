package handler

import (
	"log/slog"
	"sync"
	"time"

	"github.com/dgraph-io/ristretto/v2"
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/chat/biz"
	"github.com/onexstack/onexai/internal/chat/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	customoptions "github.com/onexstack/onexai/pkg/options"
	"github.com/onexstack/onexai/pkg/wshub"
)

// Handler aggregates all route handlers.
type Handler struct {
	biz           biz.IBiz
	hub           *wshub.Hub
	JWTSecret     string
	AnonymousAuth bool
	AuthzMode     string
	// InternalCredentials authenticates the services that write peer bindings.
	// Empty closes those endpoints — see internalAuth.
	InternalCredentials []customoptions.InternalCredential

	larkOptions *customoptions.LarkOptions
	// local is a small in-memory cache used to deduplicate Lark event retries.
	local *ristretto.Cache[string, struct{}]
	// dupMu makes the dedup check-then-act atomic across concurrent events.
	dupMu sync.Mutex
	// sendSlots bounds the WebSocket send frames being processed at once. Each
	// one becomes an agent turn, so a frame flood must not become a goroutine
	// flood.
	sendSlots chan struct{}

	// val checks REST requests. NewHandler always provides one.
	val *validation.Validator

	// statusMu guards statusCache.
	statusMu sync.Mutex
	// statusCache remembers account status for a short window so the guard does
	// not add a database round-trip to every authenticated request.
	statusCache map[string]accountStatusEntry
}

// accountStatusEntry is one account status answer and when it stops being
// trusted.
type accountStatusEntry struct {
	known   bool
	active  bool
	role    string
	expires time.Time
}

// accountStatusCacheTTL is how long a status answer is reused. It is the delay
// between an administrator disabling an account and that account's existing
// token being refused on this service.
const accountStatusCacheTTL = 30 * time.Second

// maxWebhookBodyBytes bounds the Lark webhook body. Lark events are small
// JSON documents (a message card is the largest common payload); 1 MiB is far
// above any legitimate event and far below a memory-exhaustion request.
const maxWebhookBodyBytes = 1 << 20

// dedupCapacity is how many event IDs the webhook remembers. Lark retries an
// event for up to an hour, and a busy bot sees far more than a few hundred
// events in that window. Ristretto schedules approximately ten counters per
// cost unit, and every entry costs 1.
const (
	dedupCapacity = 10_000
	dedupCounters = 10 * dedupCapacity
)

// maxConcurrentWSSends bounds sessions in flight from WebSocket frames. The
// per-session limit already serialises one conversation; this is the server
// wide ceiling that keeps a frame flood from allocating a goroutine per frame.
const maxConcurrentWSSends = 64

// Registrar defines a function signature for registering HTTP routes.
type Registrar func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc)

var registrars []Registrar

// NewHandler creates a new instance of Handler. It panics if the internal
// Ristretto deduplication cache cannot be initialised, which should only happen
// with invalid configuration (never in practice).
func NewHandler(
	b biz.IBiz,
	hub *wshub.Hub,
	jwtSecret string,
	anonymousAuth bool,
	authzMode string,
	larkOptions *customoptions.LarkOptions,
	internalCredentials []customoptions.InternalCredential,
	val *validation.Validator,
) *Handler {
	// The validator is part of the request contract, so a partly constructed
	// handler still gets one instead of silently skipping validation.
	if val == nil {
		val = validation.New(nil)
	}
	local, err := ristretto.NewCache(&ristretto.Config[string, struct{}]{
		NumCounters: dedupCounters,
		MaxCost:     dedupCapacity,
		BufferItems: 64,
	})
	if err != nil {
		panic("handler: failed to create local ristretto cache: " + err.Error())
	}

	h := &Handler{
		biz:           b,
		hub:           hub,
		JWTSecret:     jwtSecret,
		AnonymousAuth: anonymousAuth,
		AuthzMode:     authzMode,
		larkOptions:   larkOptions,

		InternalCredentials: internalCredentials,
		local:               local,
		sendSlots:           make(chan struct{}, maxConcurrentWSSends),
		val:                 val,
		statusCache:         make(map[string]accountStatusEntry),
	}
	// Wire the WebSocket frame dispatcher to the hub.
	hub.OnFrame = h.WSFrameRouter
	return h
}

// AuthMiddleware is bearer.AuthWithMode plus the account-status check.
//
// # Why it is a method rather than a package call at each route
//
// The providers used to mint an identity from any non-empty string, so "the
// token verifies" was the whole check. Now an account can be disabled while its
// token is still cryptographically valid, and every authenticated route has to
// ask the same question. A route that called bearer.AuthWithMode directly would
// silently keep admitting disabled accounts, which is exactly the kind of
// forgetting this service has been paying for elsewhere.
func (h *Handler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !bearer.Authenticate(c, h.JWTSecret, h.AnonymousAuth, h.AuthzMode) {
			return
		}
		if !h.accountUsable(c) {
			return
		}
		c.Next()
	}
}

// accountUsable refuses a disabled account and lets everything else through.
//
// An unknown user id is allowed: tokens minted by the legacy opaque providers
// and by the development bypass name no row, and treating "not in the account
// table" as disabled would break every one of them. A lookup error also allows
// the request — the alternative is that a database hiccup takes the whole
// service down — but it is logged because the status is then unknown.
func (h *Handler) accountUsable(c *gin.Context) bool {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		return true
	}

	known, active, role, ok := h.cachedAccountStatus(c, claims.UserID)
	if !ok {
		return true
	}
	if known {
		if !active {
			core.WriteResponse(c, nil, errno.ErrAccountDisabled.WithMessage("账号已被禁用，请联系管理员"))
			c.Abort()
			return false
		}
		// Recompute the account's half of the roles from the current row. The
		// external half travels in the token, so a demotion does not silently
		// revoke a control-plane grant (and vice versa).
		accountSRE := role == "sre"
		accountAdmin := role == "admin" || accountSRE
		claims.AccountRole = role
		claims.IsSRE = accountSRE || claims.ExternalSRE
		claims.IsAdmin = accountAdmin || claims.ExternalAdmin
	}
	return true
}

func (h *Handler) cachedAccountStatus(c *gin.Context, userID string) (known, active bool, role string, ok bool) {
	now := time.Now()
	h.statusMu.Lock()
	entry, hit := h.statusCache[userID]
	h.statusMu.Unlock()
	if hit && now.Before(entry.expires) {
		return entry.known, entry.active, entry.role, true
	}

	// A partly constructed Handler (tests build one directly) has no user biz;
	// there is then no account table to consult, which is the legacy behaviour.
	if h.biz == nil {
		return false, true, "", false
	}
	userBiz := h.biz.UserV1()
	if userBiz == nil {
		return false, true, "", false
	}
	known, active, role, err := userBiz.AccountStatus(c.Request.Context(), userID)
	if err != nil {
		slog.Warn("account status lookup failed; allowing the request",
			"user_id", userID, "error", err)
		return false, true, "", false
	}

	h.statusMu.Lock()
	if h.statusCache == nil {
		h.statusCache = make(map[string]accountStatusEntry)
	}
	// A bounded cache: an unbounded one would grow with every distinct token
	// this process ever saw. Expired entries are dropped opportunistically.
	if len(h.statusCache) > 10_000 {
		for id, e := range h.statusCache {
			if now.After(e.expires) {
				delete(h.statusCache, id)
			}
		}
	}
	h.statusCache[userID] = accountStatusEntry{
		known:   known,
		active:  active,
		role:    role,
		expires: now.Add(accountStatusCacheTTL),
	}
	h.statusMu.Unlock()
	return known, active, role, true
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
