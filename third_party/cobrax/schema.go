package cobrax

// ToolInput represents the decoded input for a tool invocation.
//
// After the schema format change, MCP clients send all parameters (both flags
// and positional arguments) as flat top-level properties in the JSON object.
// The FlatInput field holds the complete flat map as received from the MCP
// client.  The FlagNames and ArgNames sets are populated during tool
// registration and are used at execution time to split the flat map back into
// flag arguments and positional arguments for the underlying cobra command.
type ToolInput struct {
	// FlatInput is the complete flat parameter map received from the MCP client.
	// Both flag values and positional argument values live here, keyed by their
	// property name (e.g. "output", "verbose", "resource").
	FlatInput map[string]any

	// FlagNames is the set of property names that correspond to cobra flags.
	// Populated from the command's flag set at registration time.
	FlagNames map[string]struct{}

	// ArgNames is the ordered list of positional argument names, in the order
	// they appear in cmd.Use.  Ordering matters for positional args.
	ArgNames []string
}

// ToolOutput represents the output structure for command tools.
type ToolOutput struct {
	StdOut   string `json:"stdout,omitempty" jsonschema:"Standard output"`
	StdErr   string `json:"stderr,omitempty" jsonschema:"Standard error"`
	ExitCode int    `json:"exitCode" jsonschema:"Exit code"`
}
