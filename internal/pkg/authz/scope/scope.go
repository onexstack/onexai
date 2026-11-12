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

// Package scope expresses which objects in a collection a caller may see.
//
// It is a matching vocabulary, not a policy engine: the policy decides *what*
// scope a caller has (from a role, a rule, a token), and this decides which
// rows that scope selects. Keeping the two apart is what lets one list endpoint
// be correct for every caller without embedding role logic in it.
//
// The contract every caller must honour is the one that is easy to get wrong:
//
//   - the scope is applied **before** sorting, counting and pagination, so a
//     page never contains a row the caller may not see and a total never counts
//     one;
//   - a denial returns an empty collection or an error, never a wider one;
//   - a `list` grant does not imply `get` on an individual row — a caller that
//     names an id still goes through the per-object check.
package scope

import (
	"fmt"
	"sort"
	"strings"
)

// Kind is the shape of a scope.
type Kind string

const (
	// All admits every object. It is what a deployment with authentication
	// disabled gets, and it is deliberately explicit: an empty scope is not All.
	All Kind = "ALL"
	// None admits nothing. It is the zero value's meaning in the compiled
	// matcher, so an unset scope cannot accidentally grant everything.
	None Kind = "NONE"
	// AnyOf admits an object matching any clause.
	AnyOf Kind = "ANY_OF"
)

// Attribute names what a predicate is compared against.
type Attribute string

const (
	// AttributeNamespace is the object's namespace.
	AttributeNamespace Attribute = "namespace"
	// AttributeName is the object's own name.
	AttributeName Attribute = "name"
	// AttributeSubject is the principal the object belongs to — a session's
	// owner, a run's subject, a memory's subject.
	AttributeSubject Attribute = "subject"
)

// Operator is how a predicate compares. IN is the only one: a list of values
// is expressive enough for every scope this service builds, and adding
// operators later is additive.
type Operator string

const (
	// In admits the object when its attribute equals one of the values.
	In Operator = "IN"
)

// Scope is the declared selection.
type Scope struct {
	Kind  Kind          `json:"kind"`
	AnyOf []ScopeClause `json:"anyOf,omitempty"`
}

// ScopeClause is an AND of predicates.
type ScopeClause struct {
	All []ScopePredicate `json:"all"`
}

// ScopePredicate is one `attribute IN values` test.
type ScopePredicate struct {
	Attribute Attribute `json:"attribute"`
	Operator  Operator  `json:"operator"`
	Values    []string  `json:"values"`
}

// Object is one row's addressable identity.
type Object struct {
	Namespace string
	Name      string
	Subject   string
}

// Matcher is a compiled scope. The zero value admits nothing.
type Matcher struct {
	kind    Kind
	clauses []compiledClause
}

type compiledClause []compiledPredicate

type compiledPredicate struct {
	attribute Attribute
	values    map[string]bool
}

// Compile validates a scope and returns its matcher.
//
// Every error is fail-closed: an unknown kind, attribute or operator, an empty
// clause list, or a predicate with no values is refused, because the
// alternative — ignoring the part that could not be understood — would widen the
// collection the caller sees.
func Compile(declared Scope) (Matcher, error) {
	switch declared.Kind {
	case All:
		return Matcher{kind: All}, nil
	case None:
		return Matcher{kind: None}, nil
	case AnyOf:
		if len(declared.AnyOf) == 0 {
			return Matcher{}, fmt.Errorf("scope: ANY_OF needs at least one clause")
		}
		compiled := make([]compiledClause, 0, len(declared.AnyOf))
		for i, clause := range declared.AnyOf {
			if len(clause.All) == 0 {
				return Matcher{}, fmt.Errorf("scope: anyOf[%d] needs at least one predicate", i)
			}
			predicates := make(compiledClause, 0, len(clause.All))
			for j, predicate := range clause.All {
				if predicate.Operator != In {
					return Matcher{}, fmt.Errorf("scope: anyOf[%d].all[%d] has unsupported operator %q", i, j, predicate.Operator)
				}
				switch predicate.Attribute {
				case AttributeNamespace, AttributeName, AttributeSubject:
				default:
					return Matcher{}, fmt.Errorf("scope: anyOf[%d].all[%d] has unsupported attribute %q", i, j, predicate.Attribute)
				}
				values := make(map[string]bool, len(predicate.Values))
				for _, value := range predicate.Values {
					if strings.TrimSpace(value) == "" {
						return Matcher{}, fmt.Errorf("scope: anyOf[%d].all[%d] has an empty value", i, j)
					}
					values[value] = true
				}
				if len(values) == 0 {
					return Matcher{}, fmt.Errorf("scope: anyOf[%d].all[%d] has no values", i, j)
				}
				predicates = append(predicates, compiledPredicate{attribute: predicate.Attribute, values: values})
			}
			compiled = append(compiled, predicates)
		}
		return Matcher{kind: AnyOf, clauses: compiled}, nil
	default:
		return Matcher{}, fmt.Errorf("scope: unsupported kind %q", declared.Kind)
	}
}

// Matches reports whether the object is inside the scope.
func (m Matcher) Matches(object Object) bool {
	switch m.kind {
	case All:
		return true
	case AnyOf:
		for _, clause := range m.clauses {
			if clause.matches(object) {
				return true
			}
		}
	}
	return false
}

// Filter returns the objects the scope admits, preserving order.
func (m Matcher) Filter(objects []Object) []Object {
	if m.kind == All {
		return objects
	}
	out := make([]Object, 0, len(objects))
	for _, object := range objects {
		if m.Matches(object) {
			out = append(out, object)
		}
	}
	return out
}

// AdmitsAll reports whether the scope is All, so a caller can skip a query
// filter it does not need.
func (m Matcher) AdmitsAll() bool { return m.kind == All }

// AdmitsNothing reports whether the scope is empty, so a caller can answer
// without touching the store at all.
func (m Matcher) AdmitsNothing() bool { return m.kind != All && m.kind != AnyOf }

// Values returns the values a predicate for attribute uses, for a caller that
// wants to push the filter into SQL instead of filtering rows in memory.
//
// It returns false when the scope is not a single-clause, single-attribute
// conjunction, which is exactly the case a SQL filter cannot express: the
// caller then has to filter in memory.
func (m Matcher) Values(attribute Attribute) ([]string, bool) {
	if m.kind != AnyOf || len(m.clauses) != 1 || len(m.clauses[0]) != 1 {
		return nil, false
	}
	predicate := m.clauses[0][0]
	if predicate.attribute != attribute {
		return nil, false
	}
	values := make([]string, 0, len(predicate.values))
	for value := range predicate.values {
		values = append(values, value)
	}
	sort.Strings(values)
	return values, true
}

func (c compiledClause) matches(object Object) bool {
	for _, predicate := range c {
		if !predicate.matches(object) {
			return false
		}
	}
	return true
}

func (p compiledPredicate) matches(object Object) bool {
	switch p.attribute {
	case AttributeNamespace:
		return p.values[object.Namespace]
	case AttributeName:
		return p.values[object.Name]
	case AttributeSubject:
		return p.values[object.Subject]
	default:
		return false
	}
}
