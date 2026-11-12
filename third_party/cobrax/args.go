package cobrax

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// Cobra command annotation keys for positional argument metadata.
// Set these in cmd.Annotations to provide semantic descriptions for each
// positional argument, which are then surfaced in the MCP Tool JSON Schema
// so that LLMs know exactly what to supply for each argument position.
//
// Convention:
//
//	AnnotationArgPrefix + "0" = description for the first positional arg
//	AnnotationArgPrefix + "1" = description for the second positional arg
//	... and so on
//
// Example:
//
//	cmd.Annotations = map[string]string{
//	    cobrax.AnnotationArgPrefix + "0": "The on-call module name (e.g. 'bke', 'kafka')",
//	    cobrax.AnnotationArgPrefix + "1": "Brief title describing the incident",
//	}
const AnnotationArgPrefix = "cobrax.arg."

// ArgSpec describes a single positional argument parsed from cmd.Use.
type ArgSpec struct {
	// Name is the canonical field name used in the JSON Schema (e.g. "module", "title").
	// Derived from the bracket-stripped token in cmd.Use (e.g. "<module>" → "module").
	Name string

	// Description is a human-readable explanation of what this argument means.
	// Populated from the AnnotationArgPrefix+index annotation when available,
	// or synthesised from the usage pattern token otherwise.
	Description string

	// Required indicates whether this argument must be present.
	// true  → angle-bracket syntax  e.g. <name>
	// false → square-bracket syntax e.g. [name]
	Required bool

	// Variadic indicates this argument accepts one or more values.
	// true when the token ends with "..." (e.g. [targets...] or <files...>).
	Variadic bool

	// Index is the zero-based position of this argument in the CLI invocation.
	Index int
}

// argTokenRe matches a single positional-argument token in cmd.Use.
// It captures:
//
//	group 1: bracket type ("<" required, "[" optional)
//	group 2: argument name (stripped of trailing "...")
//	group 3: variadic marker "..." (may be empty)
//
// Examples that match:
//
//	<module>       → required, name=module
//	[title]        → optional, name=title
//	[targets...]   → optional, variadic, name=targets
//	<files...>     → required, variadic, name=files
var argTokenRe = regexp.MustCompile(`([<\[])([^>\]\s.]+)(\.{3})?[>\]]`)

// parseArgSpecs analyses cmd.Use and cmd.Annotations to produce an ordered
// slice of ArgSpec values — one per distinct positional argument token.
//
// Parsing rules for cmd.Use tokens:
//   - "<name>"      → required, single-value
//   - "[name]"      → optional, single-value
//   - "<name...>"   → required, variadic
//   - "[name...]"   → optional, variadic
//   - "[flags]"     is silently skipped (cobra's standard sentinel)
//
// Descriptions are sourced (in priority order) from:
//  1. cmd.Annotations["cobrax.arg.<index>"]  (highest priority, explicit)
//  2. Synthesised from the token and its required/variadic attributes
//
// If cmd.Use contains no argument tokens the returned slice is nil.
func parseArgSpecs(cmd *cobra.Command) []ArgSpec {
	// Strip the command name (first word) from cmd.Use.
	use := cmd.Use
	if spaceIdx := strings.IndexByte(use, ' '); spaceIdx != -1 {
		use = use[spaceIdx+1:]
	} else {
		// No arguments at all.
		return nil
	}

	// Remove the "[flags]" sentinel that cobra appends to some Use strings.
	use = strings.ReplaceAll(use, "[flags]", "")
	use = strings.TrimSpace(use)
	if use == "" {
		return nil
	}

	matches := argTokenRe.FindAllStringSubmatch(use, -1)
	if len(matches) == 0 {
		return nil
	}

	specs := make([]ArgSpec, 0, len(matches))
	for i, m := range matches {
		bracketType := m[1] // "<" or "["
		rawName := m[2]     // argument name without brackets/dots
		variadicMark := m[3]

		required := bracketType == "<"
		variadic := variadicMark == "..."

		// Normalise name: lowercase, replace non-alphanum with underscore.
		name := normaliseName(rawName)

		// Build a default description from the usage token.
		tokenRepr := reconstructToken(bracketType, rawName, variadic)
		var desc string
		if variadic {
			if required {
				desc = fmt.Sprintf("One or more %s values (required). Usage: %s", name, tokenRepr)
			} else {
				desc = fmt.Sprintf("Zero or more %s values (optional). Usage: %s", name, tokenRepr)
			}
		} else {
			if required {
				desc = fmt.Sprintf("The %s value (required). Usage: %s", name, tokenRepr)
			} else {
				desc = fmt.Sprintf("The %s value (optional). Usage: %s", name, tokenRepr)
			}
		}

		// Override description with annotation if provided.
		annotKey := fmt.Sprintf("%s%d", AnnotationArgPrefix, i)
		if annotDesc, ok := cmd.Annotations[annotKey]; ok && annotDesc != "" {
			desc = annotDesc
		}

		specs = append(specs, ArgSpec{
			Name:        name,
			Description: desc,
			Required:    required,
			Variadic:    variadic,
			Index:       i,
		})
	}

	return specs
}

// normaliseName converts a raw argument name from cmd.Use into a safe JSON
// Schema property name: lowercase letters, digits, and underscores only.
func normaliseName(raw string) string {
	raw = strings.ToLower(raw)
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// reconstructToken rebuilds a canonical usage token for display purposes.
func reconstructToken(bracketType, name string, variadic bool) string {
	open := bracketType
	close := ">"
	if bracketType == "[" {
		close = "]"
	}
	dots := ""
	if variadic {
		dots = "..."
	}
	return open + name + dots + close
}
