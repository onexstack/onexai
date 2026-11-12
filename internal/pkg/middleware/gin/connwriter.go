package gin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// connWriterKey is the gin.Context key under which CaptureConnWriter stashes
// the ResponseWriter it was handed.
const connWriterKey = "onexai:conn_writer"

// CaptureConnWriter records the request's ResponseWriter as it exists before
// any middleware wraps it.
//
// http.ResponseController reaches the underlying net.Conn by walking the
// writer's Unwrap chain, so it only works on a writer that still carries that
// chain down to net/http. Gin's own writer does; a wrapper that merely embeds
// gin.ResponseWriter does not, because the embedded interface has no Unwrap and
// the chain ends there. onexstack's Observability middleware installs exactly
// such a wrapper once its logger is at debug level, which leaves the handler
// unable to clear the server's write deadline — and net/http treats that
// deadline as absolute for the whole request, closing the connection on the
// first write past it. A streaming response that outlives the server timeout is
// then severed mid-stream, with nothing the handler can do about it.
//
// So a handler that needs the connection asks for it with ConnWriter, and this
// middleware has to be registered before anything that might wrap the writer.
func CaptureConnWriter() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(connWriterKey, c.Writer)
		c.Next()
	}
}

// ConnWriter returns the ResponseWriter recorded by CaptureConnWriter, falling
// back to the current one when the middleware is not installed.
//
// It is for connection control only — the deadlines that a long-lived response
// has to manage for itself. Writing a response must still go through c.Writer,
// or the write bypasses every middleware that wrapped it.
func ConnWriter(c *gin.Context) http.ResponseWriter {
	if v, ok := c.Get(connWriterKey); ok {
		if w, ok := v.(http.ResponseWriter); ok {
			return w
		}
	}
	return c.Writer
}
