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

package scope

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func subjectScope(subjects ...string) Scope {
	return Scope{Kind: AnyOf, AnyOf: []ScopeClause{{
		All: []ScopePredicate{{Attribute: AttributeSubject, Operator: In, Values: subjects}},
	}}}
}

func TestAllAdmitsEverything(t *testing.T) {
	matcher, err := Compile(Scope{Kind: All})
	require.NoError(t, err)
	assert.True(t, matcher.AdmitsAll())
	assert.True(t, matcher.Matches(Object{Subject: "anyone"}))
}

// TestZeroMatcherDeniesEverything pins the fail-closed default: a scope nobody
// set must not be the scope that sees the most.
func TestZeroMatcherDeniesEverything(t *testing.T) {
	var matcher Matcher
	assert.True(t, matcher.AdmitsNothing())
	assert.False(t, matcher.Matches(Object{Subject: "anyone"}))
	assert.Empty(t, matcher.Filter([]Object{{Subject: "a"}, {Subject: "b"}}))
}

func TestNoneDeniesEverything(t *testing.T) {
	matcher, err := Compile(Scope{Kind: None})
	require.NoError(t, err)
	assert.True(t, matcher.AdmitsNothing())
	assert.False(t, matcher.Matches(Object{}))
}

func TestAnyOfMatchesTheCallersOwnRows(t *testing.T) {
	matcher, err := Compile(subjectScope("u1"))
	require.NoError(t, err)

	assert.True(t, matcher.Matches(Object{Subject: "u1"}))
	assert.False(t, matcher.Matches(Object{Subject: "u2"}))

	kept := matcher.Filter([]Object{{Subject: "u1"}, {Subject: "u2"}, {Subject: "u1"}})
	require.Len(t, kept, 2)
	assert.Equal(t, "u1", kept[0].Subject)
}

// TestClauseIsAnAnd pins that predicates inside one clause all have to hold.
func TestClauseIsAnAnd(t *testing.T) {
	matcher, err := Compile(Scope{Kind: AnyOf, AnyOf: []ScopeClause{{
		All: []ScopePredicate{
			{Attribute: AttributeNamespace, Operator: In, Values: []string{"default"}},
			{Attribute: AttributeName, Operator: In, Values: []string{"session-1"}},
		},
	}}})
	require.NoError(t, err)

	assert.True(t, matcher.Matches(Object{Namespace: "default", Name: "session-1"}))
	assert.False(t, matcher.Matches(Object{Namespace: "other", Name: "session-1"}))
	assert.False(t, matcher.Matches(Object{Namespace: "default", Name: "session-2"}))
}

// TestClausesAreAnOr pins that separate clauses widen, which is the point of
// ANY_OF.
func TestClausesAreAnOr(t *testing.T) {
	matcher, err := Compile(Scope{Kind: AnyOf, AnyOf: []ScopeClause{
		{All: []ScopePredicate{{Attribute: AttributeSubject, Operator: In, Values: []string{"u1"}}}},
		{All: []ScopePredicate{{Attribute: AttributeNamespace, Operator: In, Values: []string{"shared"}}}},
	}})
	require.NoError(t, err)

	assert.True(t, matcher.Matches(Object{Subject: "u1"}))
	assert.True(t, matcher.Matches(Object{Namespace: "shared"}))
	assert.False(t, matcher.Matches(Object{Subject: "u2", Namespace: "private"}))
}

// TestEveryInvalidScopeFailsClosed pins the refusal cases: anything the compiler
// does not understand must not become "allow everything".
func TestEveryInvalidScopeFailsClosed(t *testing.T) {
	cases := map[string]Scope{
		"unknown kind":             {Kind: "SOME"},
		"anyof without clause":     {Kind: AnyOf},
		"clause without predicate": {Kind: AnyOf, AnyOf: []ScopeClause{{}}},
		"unknown attribute": {Kind: AnyOf, AnyOf: []ScopeClause{{All: []ScopePredicate{
			{Attribute: "team", Operator: In, Values: []string{"x"}}}}}},
		"unknown operator": {Kind: AnyOf, AnyOf: []ScopeClause{{All: []ScopePredicate{
			{Attribute: AttributeName, Operator: "EQUALS", Values: []string{"x"}}}}}},
		"predicate without values": {Kind: AnyOf, AnyOf: []ScopeClause{{All: []ScopePredicate{
			{Attribute: AttributeName, Operator: In}}}}},
		"blank value": {Kind: AnyOf, AnyOf: []ScopeClause{{All: []ScopePredicate{
			{Attribute: AttributeName, Operator: In, Values: []string{" "}}}}}},
	}

	for name, declared := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(declared)
			require.Error(t, err)
		})
	}
}

// TestValuesIsForSQLPushdown pins the one case a caller may push into the query,
// and that everything else reports "filter in memory".
func TestValuesIsForSQLPushdown(t *testing.T) {
	matcher, err := Compile(subjectScope("u1", "u2"))
	require.NoError(t, err)

	values, ok := matcher.Values(AttributeSubject)
	require.True(t, ok)
	assert.Equal(t, []string{"u1", "u2"}, values, "sorted so two runs build the same query")

	_, ok = matcher.Values(AttributeNamespace)
	assert.False(t, ok, "a predicate on another attribute is not this attribute's filter")

	all, err := Compile(Scope{Kind: All})
	require.NoError(t, err)
	_, ok = all.Values(AttributeSubject)
	assert.False(t, ok, "ALL is expressed by omitting the filter, not by listing values")
}
