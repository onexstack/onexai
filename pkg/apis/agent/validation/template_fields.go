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

package validation

import (
	"fmt"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// The template data a custom command sees is deliberately small. The chat
// service renders command bodies with text/template (missingkey=error), so a
// misspelled field is a runtime failure the author only sees after typing the
// command in a chat. These helpers catch the deterministic part at write time.
//
// The checking is intentionally conservative: a field reached through a
// variable, a dynamic pipeline or a range/with body whose element type is not
// known is skipped rather than rejected. The goal is to catch typos such as
// .Param or .Params.undeclared, not to build a full type checker.
const (
	templateScopeCommand = "command"
	templateScopeOutput  = "output"

	maxTemplateFieldErrors = 5
)

type scopeKind int

const (
	// scopeUnknown means the static type of dot is not known. Sub-fields are
	// skipped: rejecting them would break valid range/with bodies.
	scopeUnknown scopeKind = iota
	// scopeScalar is a value that has no addressable fields.
	scopeScalar
	// scopeFields is the root object with a fixed set of named fields.
	scopeFields
	// scopeMapKnown is a map whose keys are declared up front (Params, User,
	// Conversation).
	scopeMapKnown
	// scopeMapAny is a map/interface whose keys cannot be known statically
	// (JSON, Header, Now).
	scopeMapAny
)

// fieldScope describes one named field of a template data root.
type fieldScope struct {
	kind scopeKind
	keys map[string]struct{}
	name string
}

// templateScope is the root view for one kind of template.
type templateScope struct {
	name   string
	order  []string
	fields map[string]fieldScope
	params []string
}

// scope is the walker's current view of dot.
type scope struct {
	kind   scopeKind
	fields map[string]fieldScope
	keys   map[string]struct{}
	name   string
	order  []string
}

func (s templateScope) root() scope {
	return scope{
		kind:   scopeFields,
		fields: s.fields,
		name:   s.name,
		order:  s.order,
	}
}

func scopeFromField(f fieldScope) scope {
	return scope{kind: f.kind, keys: f.keys, name: f.name}
}

// commandTemplateScope is the data shape of TemplateData: .Params, .Command,
// .Agent, .Conversation, .User, .DryRun and .Now.
func commandTemplateScope(params []agent.CommandParameter) templateScope {
	declared, keys := declaredParameters(params)
	return templateScope{
		name:  templateScopeCommand,
		order: []string{"Params", "Command", "Agent", "Conversation", "User", "DryRun", "Now"},
		fields: map[string]fieldScope{
			"Params":       {kind: scopeMapKnown, keys: keys, name: "parameter"},
			"Command":      {kind: scopeScalar, name: "command"},
			"Agent":        {kind: scopeScalar, name: "agent"},
			"Conversation": {kind: scopeMapKnown, keys: conversationFields(), name: "conversation"},
			"User":         {kind: scopeMapKnown, keys: userFields(), name: "user"},
			"DryRun":       {kind: scopeScalar, name: "dryRun"},
			"Now":          {kind: scopeMapAny, name: "now"},
		},
		params: declared,
	}
}

// outputTemplateScope is the data shape of OutputTemplateData: .Status,
// .Header, .Body, .JSON and .Params.
func outputTemplateScope(params []agent.CommandParameter) templateScope {
	declared, keys := declaredParameters(params)
	return templateScope{
		name:  templateScopeOutput,
		order: []string{"Status", "Header", "Body", "JSON", "Params"},
		fields: map[string]fieldScope{
			"Status": {kind: scopeScalar, name: "status"},
			"Header": {kind: scopeMapAny, name: "header"},
			"Body":   {kind: scopeScalar, name: "body"},
			"JSON":   {kind: scopeMapAny, name: "json"},
			"Params": {kind: scopeMapKnown, keys: keys, name: "parameter"},
		},
		params: declared,
	}
}

func declaredParameters(params []agent.CommandParameter) ([]string, map[string]struct{}) {
	declared := make([]string, 0, len(params))
	keys := make(map[string]struct{}, len(params))
	for i := range params {
		name := params[i].Name
		if name == "" {
			continue
		}
		declared = append(declared, name)
		keys[name] = struct{}{}
	}
	return declared, keys
}

func conversationFields() map[string]struct{} {
	return map[string]struct{}{
		"conversationId": {},
		"chatType":       {},
		"surface":        {},
	}
}

func userFields() map[string]struct{} {
	return map[string]struct{}{
		"Username": {},
		"UID":      {},
		"Groups":   {},
		"Extra":    {},
	}
}

// validateTemplateFields parses nothing: the caller has already parsed the
// template. It returns one message per distinct problem, capped so a single
// bad template does not flood the admission response.
func validateTemplateFields(tmpl *template.Template, ts templateScope) []string {
	if tmpl == nil {
		return nil
	}
	v := &templateFieldValidator{
		scope: ts,
		seen:  make(map[string]struct{}),
		calls: make(map[string]*defineCall),
	}
	v.checkAll(tmpl)
	return v.errs
}

type defineCall struct {
	scope scope
	calls int
}

type templateFieldValidator struct {
	scope templateScope
	errs  []string
	seen  map[string]struct{}
	calls map[string]*defineCall
}

func (v *templateFieldValidator) checkAll(tmpl *template.Template) {
	walked := make(map[*parse.Tree]struct{})
	if tmpl.Tree != nil {
		v.walk(tmpl.Tree.Root, v.scope.root())
		walked[tmpl.Tree] = struct{}{}
	}

	// Walk associated templates in dependency order: a define is checked with
	// the scope recorded at its call site. When no call site is known yet, walk
	// one uncalled define with the root scope so dead code still catches typos;
	// that walk may in turn record call sites for other defines.
	for {
		progressed := false
		for _, t := range tmpl.Templates() {
			if t.Tree == nil {
				continue
			}
			if _, ok := walked[t.Tree]; ok {
				continue
			}
			call, ok := v.calls[t.Name()]
			if !ok {
				continue
			}
			v.walk(t.Tree.Root, call.scope)
			walked[t.Tree] = struct{}{}
			progressed = true
		}
		if progressed {
			continue
		}
		for _, t := range tmpl.Templates() {
			if t.Tree == nil {
				continue
			}
			if _, ok := walked[t.Tree]; ok {
				continue
			}
			v.walk(t.Tree.Root, v.scope.root())
			walked[t.Tree] = struct{}{}
			progressed = true
			break
		}
		if !progressed {
			return
		}
	}
}

func (v *templateFieldValidator) errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if _, ok := v.seen[msg]; ok {
		return
	}
	v.seen[msg] = struct{}{}
	if len(v.errs) < maxTemplateFieldErrors {
		v.errs = append(v.errs, msg)
	}
}

func (v *templateFieldValidator) walk(node parse.Node, dot scope) {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, child := range n.Nodes {
			v.walk(child, dot)
		}
	case *parse.ActionNode:
		v.walkPipe(n.Pipe, dot)
	case *parse.PipeNode:
		v.walkPipe(n, dot)
	case *parse.CommandNode:
		for i, arg := range n.Args {
			// The first argument of a command may be a function name, not a
			// value. Skip it; every remaining argument is a value expression.
			if i == 0 {
				if _, ok := arg.(*parse.IdentifierNode); ok {
					continue
				}
			}
			v.walk(arg, dot)
		}
	case *parse.FieldNode:
		v.checkChain(dot, n.Ident)
	case *parse.VariableNode:
		if len(n.Ident) > 1 && n.Ident[0] == "$" {
			v.checkChain(v.scope.root(), n.Ident[1:])
		}
	case *parse.ChainNode:
		// (pipeline).Field or (function).Field: the base has no static type we
		// can follow, so only walk the pipeline for root-relative references.
		if n.Node != nil {
			v.walk(n.Node, dot)
		}
	case *parse.IfNode:
		v.walkPipe(n.Pipe, dot)
		v.walk(n.List, dot)
		v.walk(n.ElseList, dot)
	case *parse.RangeNode:
		v.walkPipe(n.Pipe, dot)
		v.walk(n.List, scope{kind: scopeUnknown})
		v.walk(n.ElseList, dot)
	case *parse.WithNode:
		v.walkPipe(n.Pipe, dot)
		v.walk(n.List, v.pipeScope(n.Pipe, dot))
		v.walk(n.ElseList, dot)
	case *parse.TemplateNode:
		if n.Pipe != nil {
			v.walkPipe(n.Pipe, dot)
		}
		if n.Name != "" {
			v.recordCall(n.Name, v.pipeScope(n.Pipe, dot))
		}
	case *parse.DotNode, *parse.TextNode, *parse.CommentNode,
		*parse.BoolNode, *parse.NumberNode, *parse.StringNode,
		*parse.NilNode, *parse.IdentifierNode, *parse.BreakNode, *parse.ContinueNode:
		// Leaf nodes carry no field references.
	default:
		// A node type this package does not know: do nothing rather than
		// rejecting a template that may be valid.
	}
}

func (v *templateFieldValidator) walkPipe(pipe *parse.PipeNode, dot scope) {
	if pipe == nil {
		return
	}
	for _, cmd := range pipe.Cmds {
		v.walk(cmd, dot)
	}
}

// pipeScope returns the dot a with/range body or a template call sees.
func (v *templateFieldValidator) pipeScope(pipe *parse.PipeNode, dot scope) scope {
	if pipe == nil || len(pipe.Cmds) != 1 || len(pipe.Cmds[0].Args) != 1 {
		return scope{kind: scopeUnknown}
	}
	switch n := pipe.Cmds[0].Args[0].(type) {
	case *parse.DotNode:
		return dot
	case *parse.FieldNode:
		return v.resolve(dot, n.Ident)
	case *parse.VariableNode:
		if len(n.Ident) > 1 && n.Ident[0] == "$" {
			return v.resolve(v.scope.root(), n.Ident[1:])
		}
	}
	return scope{kind: scopeUnknown}
}

// resolve follows a field chain without reporting errors. Unknown links
// collapse to scopeUnknown.
func (v *templateFieldValidator) resolve(dot scope, idents []string) scope {
	for _, ident := range idents {
		switch dot.kind {
		case scopeFields:
			child, ok := dot.fields[ident]
			if !ok {
				return scope{kind: scopeUnknown}
			}
			dot = scopeFromField(child)
		case scopeMapKnown:
			if _, ok := dot.keys[ident]; !ok {
				return scope{kind: scopeUnknown}
			}
			dot = scope{kind: scopeUnknown}
		case scopeMapAny:
			dot = scope{kind: scopeUnknown}
		default:
			return scope{kind: scopeUnknown}
		}
	}
	return dot
}

func (v *templateFieldValidator) recordCall(name string, dot scope) {
	if call, ok := v.calls[name]; ok {
		call.calls++
		// Two call sites with different scopes cannot be represented by one
		// view; checking the define against an unknown dot is the safe choice.
		call.scope = scope{kind: scopeUnknown}
		return
	}
	v.calls[name] = &defineCall{scope: dot, calls: 1}
}

func (v *templateFieldValidator) checkChain(dot scope, idents []string) {
	if len(idents) == 0 {
		return
	}
	for i, ident := range idents {
		switch dot.kind {
		case scopeUnknown:
			return
		case scopeScalar:
			v.errorf("template field .%s is unknown; .%s is a scalar",
				strings.Join(idents[:i+1], "."), strings.Join(idents[:i], "."))
			return
		case scopeFields:
			child, ok := dot.fields[ident]
			if !ok {
				if ident == "Param" {
					msg := "template field .Param is unknown; the parameter set is .Params (plural), " +
						"e.g. {{.Params.name}}; available fields: " + strings.Join(dot.order, ", ")
					if len(v.scope.params) > 0 {
						msg += "; declared parameters: " + strings.Join(v.scope.params, ", ")
					}
					v.errorf("%s", msg)
					return
				}
				label := "available fields"
				if dot.name == templateScopeOutput {
					label = "available output fields"
				}
				v.errorf("template field .%s is unknown; %s: %s",
					strings.Join(idents[:i+1], "."), label, strings.Join(dot.order, ", "))
				return
			}
			dot = scopeFromField(child)
		case scopeMapKnown:
			if _, ok := dot.keys[ident]; !ok {
				v.errorf("template field .%s is unknown; available %s fields: %s",
					strings.Join(idents[:i+1], "."), dot.name, joinScopeKeys(dot.keys))
				return
			}
			dot = scope{kind: scopeUnknown}
		case scopeMapAny:
			dot = scope{kind: scopeUnknown}
		}
	}
}

func joinScopeKeys(keys map[string]struct{}) string {
	if len(keys) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
