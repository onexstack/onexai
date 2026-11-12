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

func TestCompileAcceptsAPortableSchema(t *testing.T) {
	canonical, digest, err := Compile([]byte(`{
		"type":"object",
		"properties":{"status":{"type":"string"},"n":{"type":"integer"}},
		"required":["status"],
		"additionalProperties":false
	}`))
	require.NoError(t, err)
	assert.NotEmpty(t, canonical)
	assert.Len(t, digest, 64)
}

func TestCompileRejectsANonObjectRoot(t *testing.T) {
	_, _, err := Compile([]byte(`{"type":"array"}`))
	require.Error(t, err)
}

func TestCompileRejectsUnknownKeywords(t *testing.T) {
	_, _, err := Compile([]byte(`{"type":"object","oneOf":[]}`))
	require.Error(t, err)
}

func TestValidator(t *testing.T) {
	canonical, _, err := Compile([]byte(`{
		"type":"object",
		"properties":{"status":{"type":"string"}},
		"required":["status"],
		"additionalProperties":false
	}`))
	require.NoError(t, err)
	validator, err := NewValidator(canonical)
	require.NoError(t, err)
	require.NoError(t, validator.ValidateJSON(`{"status":"ok"}`))
	require.Error(t, validator.ValidateJSON(`{"status":1}`))
	require.Error(t, validator.ValidateJSON(`{"status":"ok"} trailing`))
	require.Error(t, validator.ValidateJSON(`not json`))
}
