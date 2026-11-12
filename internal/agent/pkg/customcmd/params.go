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

// Package customcmd validates and runs the invocation bodies of user-defined
// slash commands. The chat service parses the command line with cobra; this
// package is the server-side second statement of the same contract.
package customcmd

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
)

// ParamError reports one argument that does not satisfy its declaration.
type ParamError struct {
	Param   string
	Message string
}

func (e *ParamError) Error() string {
	if e.Param == "" {
		return e.Message
	}
	return fmt.Sprintf("parameter %q %s", e.Param, e.Message)
}

var k8sNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// ValidateParams checks the supplied values against the declarations, applies
// defaults and returns the normalized set. Unknown keys are refused rather than
// ignored: a typo in a flag name is otherwise a silently missing argument.
func ValidateParams(declared []model.CommandParameterM, in map[string]any) (map[string]any, error) {
	known := make(map[string]struct{}, len(declared))
	out := make(map[string]any, len(declared))

	for i := range declared {
		p := &declared[i]
		known[p.Name] = struct{}{}

		value, given := in[p.Name]
		if !given {
			if p.Required {
				return nil, &ParamError{Param: p.Name, Message: "is required"}
			}
			if len(p.Default) > 0 {
				var def any
				if err := json.Unmarshal(p.Default, &def); err != nil {
					return nil, &ParamError{Param: p.Name, Message: "has an unusable default"}
				}
				value = def
			} else {
				// An omitted optional parameter is present with its type's zero
				// value. Templates are rendered with missingkey=error, so a
				// declared-but-absent key would otherwise make even
				// `{{ if .Params.flag }}` fail; a key nobody declared still
				// fails, which is the typo the option is there to catch.
				out[p.Name] = zeroValue(paramType(p))
				continue
			}
		}

		normalized, err := normalizeValue(p, value)
		if err != nil {
			return nil, err
		}
		if err := checkEnum(p, normalized); err != nil {
			return nil, err
		}
		if err := checkFormat(p, normalized); err != nil {
			return nil, err
		}
		if len(p.Schema) > 0 {
			if err := validateSchema(p.Schema, normalized); err != nil {
				return nil, &ParamError{Param: p.Name, Message: err.Error()}
			}
		}
		out[p.Name] = normalized
	}

	var unknown []string
	for name := range in {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &ParamError{Message: "unknown parameter(s): " + strings.Join(unknown, ", ")}
	}
	return out, nil
}

func paramType(p *model.CommandParameterM) string {
	if p.Type == "" {
		return "string"
	}
	return p.Type
}

// zeroValue is what a template sees for an omitted optional parameter.
func zeroValue(ptype string) any {
	switch ptype {
	case "integer":
		return int64(0)
	case "number":
		return float64(0)
	case "boolean":
		return false
	case "duration":
		return "0s"
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	case "json":
		return nil
	default:
		return ""
	}
}

func normalizeValue(p *model.CommandParameterM, value any) (any, error) {
	ptype := paramType(p)
	switch ptype {
	case "string":
		if v, ok := value.(string); ok {
			return v, nil
		}
	case "integer":
		if f, ok := numberOf(value); ok && f == math.Trunc(f) {
			return int64(f), nil
		}
	case "number":
		if f, ok := numberOf(value); ok {
			return f, nil
		}
	case "boolean":
		if v, ok := value.(bool); ok {
			return v, nil
		}
	case "duration":
		if v, ok := value.(string); ok {
			if _, err := time.ParseDuration(v); err == nil {
				return v, nil
			}
			return nil, &ParamError{Param: p.Name, Message: "must be a duration such as 30s or 2m"}
		}
	case "array":
		if v, ok := value.([]any); ok {
			return v, nil
		}
		if v, ok := value.([]string); ok {
			out := make([]any, 0, len(v))
			for _, item := range v {
				out = append(out, item)
			}
			return out, nil
		}
	case "object":
		if v, ok := value.(map[string]any); ok {
			return v, nil
		}
	case "json":
		return value, nil
	}
	return nil, &ParamError{Param: p.Name, Message: fmt.Sprintf("must be of type %s", ptype)}
}

func numberOf(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func checkEnum(p *model.CommandParameterM, value any) error {
	if len(p.Enum) == 0 {
		return nil
	}
	// Enum values are strings in the declaration, so a non-string argument is
	// compared by its textual form (3, 2.5, true). That keeps an integer or
	// boolean enum usable instead of rejecting it for being the wrong Go type.
	text := fmt.Sprint(value)
	for _, allowed := range p.Enum {
		if text == allowed {
			return nil
		}
	}
	return &ParamError{Param: p.Name, Message: "must be one of [" + strings.Join(p.Enum, ", ") + "]"}
}

func checkFormat(p *model.CommandParameterM, value any) error {
	if p.Format == "" {
		return nil
	}
	s, ok := value.(string)
	if !ok {
		return &ParamError{Param: p.Name, Message: "must be a string for format " + p.Format}
	}
	switch p.Format {
	case "k8s-name":
		if !k8sNamePattern.MatchString(s) {
			return &ParamError{Param: p.Name, Message: "must be a Kubernetes resource name"}
		}
	case "url":
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return &ParamError{Param: p.Name, Message: "must be an http or https URL"}
		}
	case "json":
		if !json.Valid([]byte(s)) {
			return &ParamError{Param: p.Name, Message: "must be valid JSON"}
		}
	case "duration":
		if _, err := time.ParseDuration(s); err != nil {
			return &ParamError{Param: p.Name, Message: "must be a duration such as 30s or 2m"}
		}
	}
	return nil
}

// validateSchema checks the supported JSON Schema subset: type, required,
// properties, items and enum. The declaration is the contract the chat service
// turns into flags, so this is deliberately smaller than full JSON Schema.
func validateSchema(schema json.RawMessage, value any) error {
	var node map[string]any
	if err := json.Unmarshal(schema, &node); err != nil {
		return fmt.Errorf("has an unusable schema")
	}
	return validateSchemaNode(node, value, "value")
}

func validateSchemaNode(node map[string]any, value any, path string) error {
	if want, ok := node["type"].(string); ok && !matchesSchemaType(want, value) {
		return fmt.Errorf("must be of type %s at %s", want, path)
	}
	if enum, ok := node["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if fmt.Sprint(candidate) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("must be one of %v at %s", enum, path)
		}
	}
	if obj, ok := value.(map[string]any); ok {
		if required, ok := node["required"].([]any); ok {
			for _, name := range required {
				if _, present := obj[fmt.Sprint(name)]; !present {
					return fmt.Errorf("is missing required field %q at %s", fmt.Sprint(name), path)
				}
			}
		}
		if props, ok := node["properties"].(map[string]any); ok {
			for name, raw := range props {
				child, present := obj[name]
				if !present {
					continue
				}
				childNode, _ := raw.(map[string]any)
				if childNode != nil {
					if err := validateSchemaNode(childNode, child, path+"."+name); err != nil {
						return err
					}
				}
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		if array, ok := value.([]any); ok {
			for i, item := range array {
				if err := validateSchemaNode(items, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func matchesSchemaType(want string, value any) bool {
	switch want {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := numberOf(value)
		return ok
	case "integer":
		f, ok := numberOf(value)
		return ok && f == math.Trunc(f)
	case "null":
		return value == nil
	default:
		return true
	}
}
