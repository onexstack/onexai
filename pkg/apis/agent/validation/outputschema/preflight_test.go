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

package outputschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compile(t *testing.T, raw string) []byte {
	t.Helper()
	canonical, _, err := Compile([]byte(raw))
	require.NoError(t, err)
	return canonical
}

// TestWordBoundaryIsRefused pins the provider-portability rule: not every model
// supports \b, and a schema that uses it fails at generation time, after the run
// has been paid for.
func TestWordBoundaryIsRefused(t *testing.T) {
	schema := compile(t, `{"type":"object","additionalProperties":false,"properties":{
		"name":{"type":"string","pattern":"\\bword\\b"}},"required":["name"]}`)

	err := CheckPortable(schema, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "word boundary")
}

// TestUncompilableRegexIsRefusedEarlier pins the layering: a pattern that does
// not compile is caught by Compile (the portable profile resolves it), so it
// never reaches the portability check.
func TestUncompilableRegexIsRefusedEarlier(t *testing.T) {
	_, _, err := Compile([]byte(`{"type":"object","additionalProperties":false,"properties":{
		"name":{"type":"string","pattern":"(unclosed"}},"required":["name"]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regexp")
}

func TestOrdinaryPatternPasses(t *testing.T) {
	schema := compile(t, `{"type":"object","additionalProperties":false,"properties":{
		"code":{"type":"string","pattern":"^[A-Z]{3}-[0-9]+$"}},"required":["code"]}`)

	assert.NoError(t, CheckPortable(schema, false))
	assert.NoError(t, CheckPortable(schema, true))
}

// TestStrictModeRequiresTheWholeContract pins what strict decoding demands: a
// closed object whose every property is required. Only checked when the agent
// asked for strict, because those constraints would reject perfectly usable
// schemas otherwise.
func TestStrictModeRequiresTheWholeContract(t *testing.T) {
	loose := compile(t, `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`)

	assert.NoError(t, CheckPortable(loose, false), "normal mode does not need the strict contract")

	err := CheckPortable(loose, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "additionalProperties")
}

func TestStrictModeRequiresEveryProperty(t *testing.T) {
	schema := compile(t, `{"type":"object","additionalProperties":false,
		"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"]}`)

	err := CheckPortable(schema, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"b"`)
}

func TestStrictAcceptsACompleteContract(t *testing.T) {
	schema := compile(t, `{"type":"object","additionalProperties":false,
		"properties":{"a":{"type":"object","additionalProperties":false,
		"properties":{"b":{"type":"string"}},"required":["b"]}},"required":["a"]}`)

	assert.NoError(t, CheckPortable(schema, true))
}

// TestNestedNodesAreChecked pins that the walk descends: a constraint only
// enforced at the root is not enforced at all.
func TestNestedNodesAreChecked(t *testing.T) {
	schema := compile(t, `{"type":"object","additionalProperties":false,"properties":{
		"items":{"type":"array","items":{"type":"object","additionalProperties":false,
		"properties":{"name":{"type":"string","pattern":"\\bname\\b"}},"required":["name"]}}},
		"required":["items"]}`)

	err := CheckPortable(schema, false)
	require.Error(t, err, "a pattern inside items must be checked too")
}
