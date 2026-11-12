package contextx

import (
	"context"
	"log/slog"
)

// contextKey is an unexported type for context keys.
// This prevents collisions with keys defined in other packages.
type contextKey string

const (
	// userIDKey is the context key for storing and retrieving a user's ID.
	userIDKey contextKey = "userID"
	// agentNamesKey is the context key for the agents a call runs as.
	agentNamesKey contextKey = "agentNames"
	// usernameKey is the context key for storing and retrieving a user's name.
	usernameKey contextKey = "username"
	// accessTokenKey is the context key for storing and retrieving an access token.
	accessTokenKey contextKey = "accessToken"
	// requestIDKey is the context key for storing and retrieving a request identifier.
	requestIDKey contextKey = "requestID"
	// traceIDKey is the context key for storing and retrieving a trace identifier.
	traceIDKey contextKey = "traceID"
	// loggerKey is the context key for storing and retrieving a structured logger.
	loggerKey contextKey = "logger"
)

// WithUserID returns a new context with the given user ID.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserID retrieves the user ID from the context.
// Returns an empty string if the user ID is not found.
func UserID(ctx context.Context) string {
	val, ok := ctx.Value(userIDKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithAgentNames returns a new context carrying the agents this call runs as.
//
// The set travels with the call because a downstream MCP server scopes its tools
// to it: oai-chat-apiserver narrows the tools it advertises to what these agents
// were granted, on the tool discovery as well as on each call. It is the same
// selection the run's authorization is judged by, so carrying it here is what
// keeps the menu a model sees and the tools it is allowed to call in agreement.
//
// An empty set is a no-op: a caller with no agent context must not gain one, and
// two callers that both name nothing are the same caller in this respect.
func WithAgentNames(ctx context.Context, agents []string) context.Context {
	if len(agents) == 0 {
		return ctx
	}
	return context.WithValue(ctx, agentNamesKey, append([]string(nil), agents...))
}

// AgentNames retrieves the agents this call runs as, or nil when none is set.
func AgentNames(ctx context.Context) []string {
	agents, _ := ctx.Value(agentNamesKey).([]string)
	return append([]string(nil), agents...)
}

// WithUsername returns a new context with the given username.
func WithUsername(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, usernameKey, username)
}

// Username retrieves the username from the context.
// Returns an empty string if the username is not found.
func Username(ctx context.Context) string {
	val, ok := ctx.Value(usernameKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithAccessToken returns a new context with the given access token.
func WithAccessToken(ctx context.Context, accessToken string) context.Context {
	return context.WithValue(ctx, accessTokenKey, accessToken)
}

// AccessToken retrieves the access token from the context.
// Returns an empty string if the access token is not found.
func AccessToken(ctx context.Context) string {
	val, ok := ctx.Value(accessTokenKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithRequestID returns a new context with the given request ID.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestID retrieves the request ID from the context.
// Returns an empty string if the request ID is not found.
func RequestID(ctx context.Context) string {
	val, ok := ctx.Value(requestIDKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithTraceID returns a new context with the given trace ID.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceID retrieves the trace ID from the context.
// Returns an empty string if the trace ID is not found.
func TraceID(ctx context.Context) string {
	val, ok := ctx.Value(traceIDKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithLogger returns a new context with the given structured logger.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// Logger retrieves the structured logger from the context.
// If no logger is found in the context, it returns slog.Default().
func Logger(ctx context.Context) *slog.Logger {
	val, ok := ctx.Value(loggerKey).(*slog.Logger)
	if !ok || val == nil {
		return slog.Default()
	}
	return val
}

// L is a short alias for Logger, retrieving the structured logger from the context.
// If no logger is found, it returns slog.Default().
func L(ctx context.Context) *slog.Logger {
	return Logger(ctx)
}
