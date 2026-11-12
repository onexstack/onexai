package gin

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/contextx"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// Authorizer defines the interface for authorization implementation.
type Authorizer interface {
	Authorize(subject, object, action string) (bool, error)
}

// AuthzMiddleware is a Gin middleware for request authorization.
func AuthzMiddleware(authorizer Authorizer) gin.HandlerFunc {
	return func(c *gin.Context) {
		subject := contextx.UserID(c.Request.Context())
		object := c.Request.URL.Path
		action := c.Request.Method

		// Log authorization context information.
		slog.Info("build authorize context", "subject", subject, "object", object, "action", action)

		// Call the authorization interface for verification.
		allowed, err := authorizer.Authorize(subject, object, action)
		if err != nil {
			// A backend outage is not a permission decision: answering 403 would
			// tell the caller to stop trying, and would put the backend error in
			// the response. The cause goes to the log, the caller gets 500.
			slog.ErrorContext(c.Request.Context(), "authorization backend failed", "error", err)
			core.WriteResponse(c, nil, errno.ErrInternal)
			c.Abort()
			return
		}
		if !allowed {
			core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage(
				"access denied: subject=%s, object=%s, action=%s",
				subject,
				object,
				action,
			))
			c.Abort()
			return
		}

		c.Next() // Continue processing the request.
	}
}
