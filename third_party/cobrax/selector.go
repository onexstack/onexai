package cobrax

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/feizaonet/cobrax/internal/bridge/flags"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CmdSelector determines if a command should become an MCP tool.
// Return true to include the command as a tool.
// Note: Basic safety filters (hidden, deprecated, non-runnable) are always applied first.
// Commands are tested against selectors in order; the first matching selector wins.
type CmdSelector func(*cobra.Command) bool

// FlagSelector determines if a flag should be included in an MCP tool.
// Return true to include the flag.
// Note: Hidden and deprecated flags are always excluded regardless of this selector.
// This selector is only applied to commands that match the associated CmdSelector.
type FlagSelector func(*pflag.Flag) bool

// MiddlewareFunc is middleware hook that runs after each tool call
// Common uses: error handling, response filtering, metrics collection.
type MiddlewareFunc func(context.Context, mcp.CallToolRequest, ToolInput, ExecuteFunc) (*mcp.CallToolResult, ToolOutput, error)

// ExecuteFunc defines the function signature for executing a tool.
type ExecuteFunc func(context.Context, mcp.CallToolRequest, ToolInput) (*mcp.CallToolResult, ToolOutput, error)

// Selector contains selectors for filtering commands and flags.
// When multiple selectors are configured, they are evaluated in order.
// The first selector whose CmdSelector matches a command is used,
// and its FlagSelector determines which flags are included for that command.
//
// Basic safety filters are always applied automatically:
//   - Hidden/deprecated commands and flags are excluded
//   - Non-runnable commands are excluded
//   - Built-in commands (mcp, help, completion) are excluded
//
// This allows fine-grained control within safe boundaries, such as:
//   - Exposing different flags for different command groups
//   - Applying stricter flag filtering to dangerous commands
//   - Having a default catch-all selector with common flag exclusions
type Selector struct {
	// CmdSelector determines if this selector applies to a command.
	// If nil, accepts all commands that pass basic safety filters.
	// Cannot be used to bypass safety filters (hidden, deprecated, non-runnable).
	CmdSelector CmdSelector

	// LocalFlagSelector determines which flags to include for commands matched by CmdSelector.
	// If nil, includes all flags that pass basic safety filters.
	// Cannot be used to bypass safety filters (hidden, deprecated flags).
	LocalFlagSelector FlagSelector

	// InheritedFlagSelector determines which persistent flags to include for commands matched by CmdSelector.
	// If nil, includes all flags that pass basic safety filters.
	// Cannot be used to bypass safety filters (hidden, deprecated flags).
	InheritedFlagSelector FlagSelector

	// Middleware is an optional middleware hook that wraps around tool execution.
	// Common uses: error handling, response filtering, metrics collection.
	// If nil, no middleware is applied.
	Middleware MiddlewareFunc
}

// toolMeta holds per-tool metadata computed at registration time and needed
// during execution to split the flat MCP input back into flags and positional
// arguments for the underlying cobra command.
type toolMeta struct {
	// flagNames is the set of flag property names included in the tool schema.
	flagNames map[string]struct{}
	// argSpecs is the ordered list of positional argument specs.
	argSpecs []ArgSpec
}

// buildFlatSchema constructs a flat JSON Schema for a cobra command.
// All flags and positional arguments appear as direct properties of the top-level
// object — there are no nested "flags" or "args" sub-objects.
// The returned toolMeta carries the flag name set and arg spec slice needed at
// execution time to reconstruct the cobra command arguments from the flat input.
func (s Selector) buildFlatSchema(cmd *cobra.Command) (*jsonschema.Schema, toolMeta) {
	schema := &jsonschema.Schema{
		Type:       "object",
		Properties: make(map[string]*jsonschema.Schema),
	}

	meta := toolMeta{
		flagNames: make(map[string]struct{}),
	}

	// basic filters: skip hidden, deprecated, and the built-in help flag.
	// help is cobra-internal and not useful as an MCP tool parameter.
	filter := func(flag *pflag.Flag) bool {
		return flag.Name == "help" || flag.Hidden || flag.Deprecated != ""
	}

	// Process local flags — add directly to the top-level schema.
	cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		if filter(flag) {
			return
		}
		if s.LocalFlagSelector != nil && !s.LocalFlagSelector(flag) {
			return
		}
		flags.AddFlagToSchema(schema, flag)
		meta.flagNames[flag.Name] = struct{}{}
	})

	// Process inherited flags — add directly to the top-level schema.
	cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
		// Skip if already added as local flag.
		if _, exists := schema.Properties[flag.Name]; exists {
			return
		}
		if filter(flag) {
			return
		}
		if s.InheritedFlagSelector != nil && !s.InheritedFlagSelector(flag) {
			return
		}
		flags.AddFlagToSchema(schema, flag)
		meta.flagNames[flag.Name] = struct{}{}
	})

	// Add positional argument properties directly to the top-level schema.
	specs := parseArgSpecs(cmd)
	meta.argSpecs = specs
	for _, spec := range specs {
		var propSchema *jsonschema.Schema
		if spec.Variadic {
			propSchema = &jsonschema.Schema{
				Type:        "array",
				Description: spec.Description,
				Items:       &jsonschema.Schema{Type: "string"},
			}
		} else {
			propSchema = &jsonschema.Schema{
				Type:        "string",
				Description: spec.Description,
			}
		}
		schema.Properties[spec.Name] = propSchema
		if spec.Required {
			schema.Required = append(schema.Required, spec.Name)
		}
	}

	// Disallow unexpected extra fields.
	schema.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}

	return schema, meta
}

// createToolFromCmd creates an MCP tool from a Cobra command.
// The toolNamePrefix is used to replace the root command name in the tool name.
//
// The generated input schema is flat: all flags and positional arguments appear
// as direct top-level properties with no nested "flags" or "args" sub-objects.
//
// The returned toolMeta carries the information needed at execution time to
// reconstruct the CLI command from the flat MCP input.
func (s Selector) createToolFromCmd(cmd *cobra.Command, toolNamePrefix string) (*mcp.Tool, toolMeta) {
	schema, meta := s.buildFlatSchema(cmd)

	// Serialize the jsonschema.Schema to json.RawMessage for use with mark3labs/mcp-go.
	rawSchema, err := json.Marshal(schema)
	if err != nil {
		// Fallback to empty object schema on marshalling error.
		rawSchema = json.RawMessage(`{"type":"object"}`)
	}

	tool := &mcp.Tool{
		Name:           toolName(cmd, toolNamePrefix),
		Description:    toolDescription(cmd),
		RawInputSchema: rawSchema,
	}

	// Apply annotations if present.
	if ann := toolAnnotations(cmd); ann != nil {
		tool.Annotations = *ann
	}

	return tool, meta
}

// toolName creates a tool name from the command path.
// The toolNamePrefix replaces the root command name in the path.
// For example, if the command path is "omnistrate-ctl cost by-cell list" and
// toolNamePrefix is "omctl", the result is "omctl_cost_by-cell_list".
func toolName(cmd *cobra.Command, toolNamePrefix string) string {
	path := cmd.CommandPath()

	// Replace the root command name with the prefix.
	// The root command name is the first word in the path.
	if spaceIdx := strings.IndexByte(path, ' '); spaceIdx != -1 {
		rest := path[spaceIdx+1:] // sub-command path without the leading space
		if toolNamePrefix == "" {
			// No prefix: use the sub-command path directly.
			path = rest
		} else {
			path = toolNamePrefix + "_" + rest
		}
	} else {
		// Single command (root command itself).
		path = toolNamePrefix
	}

	return strings.ReplaceAll(path, " ", "_")
}

// toolDescription creates a comprehensive tool description.
func toolDescription(cmd *cobra.Command) string {
	var parts []string

	// Use Long description if available, otherwise Short
	if cmd.Long != "" {
		parts = append(parts, cmd.Long)
	} else if cmd.Short != "" {
		parts = append(parts, cmd.Short)
	} else {
		parts = append(parts, fmt.Sprintf("Execute the %s command", cmd.Name()))
	}

	// Add examples if available
	if cmd.Example != "" {
		parts = append(parts, fmt.Sprintf("Examples:\n%s", cmd.Example))
	}

	return strings.Join(parts, "\n")
}

func (s *Selector) execute(ctx context.Context, request mcp.CallToolRequest, input ToolInput) (_ *mcp.CallToolResult, _ ToolOutput, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	if s.Middleware != nil {
		return s.Middleware(ctx, request, input, execute)
	}

	return execute(ctx, request, input)
}
