package known

// Package known provides a collection of well-known and commonly used constants
// throughout the application, including HTTP/gRPC header keys and general
// configuration values.

// headerKey is an unexported type for HTTP/gRPC header keys to prevent accidental
// misuse of arbitrary strings as header names.
type headerKey string

const (
	// XRequestID represents the custom HTTP/gRPC header key for a request identifier.
	// Its value is "x-request-id".
	XRequestID headerKey = "x-request-id"

	// XUserID represents the custom HTTP/gRPC header key for the ID of the
	// requesting user. This ID is expected to be unique throughout the user's lifecycle.
	// Its value is "x-user-id".
	XUserID headerKey = "x-user-id"

	// XUsername represents the custom HTTP/gRPC header key for the requesting username.
	// Its value is "x-username".
	XUsername headerKey = "x-username"

	// XAgentNames carries the agents a call runs as, comma-separated, and is the
	// wire form of "which agent is asking" for an MCP server.
	//
	// oai-agent-apiserver stamps it on every request to an MCP server — the tool
	// discovery as well as each tool call — so a server that scopes its tools per
	// agent (the chat server narrows them to the commands the agents were granted)
	// can answer with the right set. A run may select several agents, hence the
	// plural, and the values are the same agent names the run's authorization is
	// judged by.
	//
	// It is load-bearing, not a hint: the chat server's grant answer is deny-by-
	// default, so a caller that omits it names no agent, is granted nothing, and
	// is shown almost no tools — only the ones no catalogued command backs (the
	// MCP-only askai entry among them). A caller that means to use tools has to
	// say whose they are. Its value is "x-agent-names".
	XAgentNames headerKey = "x-agent-names"
)

// Application-specific constants.
const (
	// AdminUsername represents the default username for an administrative user.
	// Its value is "root".
	AdminUsername = "root"

	// MaxErrGroupConcurrency defines the maximum number of concurrent tasks
	// allowed within an errgroup. This limits simultaneous Goroutine execution
	// to prevent resource exhaustion and enhance program stability.
	// The default value is 1000, which can be adjusted based on specific
	// scenario and performance requirements.
	MaxErrGroupConcurrency = 1000
)

const (
	EncryptKey = "8a4e9b1f-uai82-f8a3-9b7e1c4d5f2a"
)

// AdminUserID is the reserved ID of the administrator user.
const AdminUserID = "user-admin"

// SkipVerifyAnnotation is the annotation that bypasses validation.
const SkipVerifyAnnotation = "onexai.io/skip-verify"
