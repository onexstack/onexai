// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package commandusage renders the canonical invocation shape of a custom
// command from its parameter declarations.
//
// The chat service and the agent service both need the same line. spec.usage
// is free-form and is shown as an example only; deriving the line from the
// declarations is what keeps error messages honest when the two disagree.
package commandusage

import "strings"

// Parameter is the subset of a declared parameter the usage line needs.
type Parameter struct {
	Name     string
	In       string // "" is flag, matching the CRD default.
	Type     string // "" is string, matching the CRD default.
	Required bool
}

// Line returns the canonical usage, e.g. "/hello <name>" or
// "/deploy <service> [--env=<env>] [--dry-run]".
func Line(name string, params []Parameter) string {
	args := Arguments(params)
	if args == "" {
		return "/" + name
	}
	return "/" + name + " " + args
}

// Arguments returns only the part after the command name, e.g.
// "<name> [--formal]". spec.usage stores this shape (the console renders the
// command name in its own column), so filling an empty spec.usage must not
// repeat the name.
//
// Positional parameters come first, in declaration order. Flags follow, also
// in declaration order. Required values are bare, optional values are
// bracketed; booleans never take a value.
func Arguments(params []Parameter) string {
	parts := make([]string, 0, len(params))

	// Positional parameters first, in declaration order.
	for i := range params {
		p := &params[i]
		if p.Name == "" || p.In != "positional" {
			continue
		}
		if p.Required {
			parts = append(parts, "<"+p.Name+">")
		} else {
			parts = append(parts, "["+p.Name+"]")
		}
	}

	// Flags follow, also in declaration order.
	for i := range params {
		p := &params[i]
		if p.Name == "" || p.In == "positional" {
			continue
		}
		parts = append(parts, flagToken(p))
	}
	return strings.Join(parts, " ")
}

func flagToken(p *Parameter) string {
	typ := p.Type
	if typ == "" {
		typ = "string"
	}
	var token string
	switch typ {
	case "boolean":
		token = "--" + p.Name
	case "array":
		token = "--" + p.Name + "=<" + p.Name + ">,..."
	default:
		token = "--" + p.Name + "=<" + p.Name + ">"
	}
	if p.Required {
		return token
	}
	return "[" + token + "]"
}
