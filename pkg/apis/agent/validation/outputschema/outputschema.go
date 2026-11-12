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
//
// The portable profile follows kagent's output-schema contract (Apache-2.0):
// one JSON object at the root, a small keyword set, non-recursive local
// references, and the string formats both OpenAI and Anthropic document.

// Package outputschema validates an agent's structured-output contract.
package outputschema

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// maxSchemaBytes bounds one schema. A schema is prompt material; a megabyte of
// it is a configuration mistake rather than a contract.
const maxSchemaBytes = 64 << 10

var supportedStringFormats = []string{
	"date", "date-time", "duration", "email", "hostname", "ipv4", "ipv6", "time", "uuid",
}

const metaSchemaTemplate = `{
	"$schema":"https://json-schema.org/draft/2020-12/schema",
	"$defs":{
		"schema":{
			"type":"object",
			"properties":{
				"$id":{"type":"string"},
				"$schema":{"type":"string"},
				"$defs":{"type":"object","additionalProperties":{"$ref":"#/$defs/schema"}},
				"$ref":{"type":"string","pattern":"^#/\\$defs/[^/]*$"},
				"additionalProperties":{"anyOf":[{"type":"boolean"},{"$ref":"#/$defs/schema"}]},
				"anyOf":{"type":"array","minItems":1,"items":{"$ref":"#/$defs/schema"}},
				"const":{},
				"description":{"type":"string"},
				"enum":{"type":"array","minItems":1},
				"format":{"enum":%s},
				"items":{"$ref":"#/$defs/schema"},
				"pattern":{"type":"string"},
				"properties":{"type":"object","additionalProperties":{"$ref":"#/$defs/schema"}},
				"required":{"type":"array","items":{"type":"string","minLength":1},"uniqueItems":true},
				"title":{"type":"string"},
				"type":{"anyOf":[{"$ref":"#/$defs/typeName"},{"type":"array","minItems":1,"uniqueItems":true,"items":{"$ref":"#/$defs/typeName"}}]}
			},
			"additionalProperties":false,
			"dependentSchemas":{
				"format":{"$ref":"#/$defs/stringSchema"},
				"pattern":{"$ref":"#/$defs/stringSchema"}
			}
		},
		"typeName":{"enum":["object","array","string","number","integer","boolean","null"]},
		"stringSchema":{"properties":{"type":{"anyOf":[{"const":"string"},{"type":"array","contains":{"const":"string"}}]}},"required":["type"]}
	},
	"allOf":[{"$ref":"#/$defs/schema"}],
	"properties":{"type":{"const":"object"}},
	"required":["type"]
}`

var resolveMetaSchema = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	formats, err := json.Marshal(supportedStringFormats)
	if err != nil {
		return nil, fmt.Errorf("encode formats: %w", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(fmt.Appendf(nil, metaSchemaTemplate, formats), &schema); err != nil {
		return nil, fmt.Errorf("decode portable meta-schema: %w", err)
	}
	return schema.Resolve(nil)
})

// Compile validates a schema against the portable profile and returns its
// canonical form and digest. The digest is what a revision carries.
func Compile(raw []byte) (canonical []byte, digest string, err error) {
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("output schema is empty")
	}
	if len(raw) > maxSchemaBytes {
		return nil, "", fmt.Errorf("output schema exceeds %d bytes", maxSchemaBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, "", fmt.Errorf("decode output schema: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, "", fmt.Errorf("output schema must contain one JSON value")
		}
		return nil, "", fmt.Errorf("decode trailing output schema: %w", err)
	}
	meta, err := resolveMetaSchema()
	if err != nil {
		return nil, "", err
	}
	if err := meta.Validate(document); err != nil {
		return nil, "", fmt.Errorf("output schema does not match the portable profile: %w", err)
	}
	canonical, err = json.Marshal(document)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize output schema: %w", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(canonical, &schema); err != nil {
		return nil, "", fmt.Errorf("parse output schema: %w", err)
	}
	if _, err := schema.Resolve(nil); err != nil {
		return nil, "", fmt.Errorf("resolve output schema: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

// Validator checks a final answer against a compiled schema.
type Validator struct {
	resolved *jsonschema.Resolved
	digest   string
}

// NewValidator resolves a canonical schema.
func NewValidator(canonical []byte) (*Validator, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(canonical, &schema); err != nil {
		return nil, fmt.Errorf("parse output schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve output schema: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return &Validator{resolved: resolved, digest: hex.EncodeToString(sum[:])}, nil
}

// Digest returns the schema digest.
func (v *Validator) Digest() string { return v.digest }

// ValidateJSON parses one complete JSON value and validates it. A partial or
// trailing value is an error, not a value to repair: the contract is one answer.
func (v *Validator) ValidateJSON(text string) error {
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("answer is not JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("answer contains more than one JSON value")
	}
	return v.resolved.Validate(value)
}
