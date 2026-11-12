package handler

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// DefaultSurface is the surface name consulted for a caller no entry names. It
// is a surface rather than a fallback field so that "everyone gets 8192, except
// the group chat which gets 2048" is one map with two keys, not two places a
// number can live.
const DefaultSurface = "default"

// OutputCaps resolves how many tokens a run started by a given caller may
// generate. It is the deployment's `agent.max-output-tokens` plus the api-key →
// surface mapping in `agent.surface-keys`, and it exists because the ceiling
// depends on who is asking while the model instance does not.
//
// Why the surface, and not the request: a run's output ceiling is a property of
// where the answer will be read — a Lark card cannot show a 4000-token essay,
// a console page can. That is known before the run starts and never changes
// during it, so it is resolved once, at the edge, and carried down the context
// rather than passed through every layer that has no opinion about it.
type OutputCaps struct {
	// BySurface maps a surface name to its ceiling in tokens. Values are >= 0;
	// 0 means "no ceiling of our own", which lets llm.max-tokens decide.
	BySurface map[string]int
	// SurfaceKeys maps a caller's AK/SK api-key to its surface name. A caller
	// absent from it is capped as DefaultSurface.
	SurfaceKeys map[string]string
}

// Ceiling returns the output ceiling for a caller named by its AK/SK api-key,
// or 0 when neither the caller's surface nor the default declares one.
func (c *OutputCaps) Ceiling(caller string) int {
	if c == nil {
		return 0
	}
	if surface, ok := c.SurfaceKeys[caller]; ok {
		if n, ok := c.BySurface[surface]; ok {
			return n
		}
	}
	return c.BySurface[DefaultSurface]
}

// callerKey names the service that sent a request, as the api-key it
// authenticated with, or "" when the caller is not a service.
//
// It reads the authorization subject the AK/SK middleware installed, which is
// the only place a request still says *which service* asked: the agent flow
// deliberately overwrites the subject with the end user once a run begins (see
// pkg/authn's note), because that is the identity the tools must act as. A
// request that never reached a run therefore still carries the service, and one
// that is about to start a run is read here — at the edge — precisely because
// after this point the answer is gone.
//
// A caller that is not a service (the admin surface authenticates a person with
// a bearer token) returns "", which resolves to the default ceiling: an
// operator debugging a run gets the same ceiling as the deployment's own
// default rather than an unnamed caller's.
func callerKey(ctx context.Context) string {
	sub, ok := authz.SubjectFromContext(ctx)
	if !ok || sub.Kind != authz.SubjectKindServiceAccount {
		return ""
	}
	return sub.ServiceName
}

// askContext returns the request context with this run's output ceiling
// attached. It is called at every site that starts a run, so that the ceiling
// is decided in one place and a new surface cannot forget it.
func (h *Handler) askContext(c *gin.Context) context.Context {
	return chatmodel.WithMaxOutputTokens(c.Request.Context(), h.outputCaps.Ceiling(callerKey(c.Request.Context())))
}
