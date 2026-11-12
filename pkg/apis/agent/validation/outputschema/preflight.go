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
	"encoding/json"
	"fmt"
	"regexp/syntax"
	"sort"
	"strings"
)

// maxPatternInstructions bounds one compiled pattern. A pattern is prompt
// material, and a pathological one can make a provider reject the whole
// request — long after the run has been paid for.
const maxPatternInstructions = 100_000

// CheckPortable refuses a schema a provider would reject at request time.
//
// The portable profile (Compile) answers "is this a well-formed contract for
// us". This answers the different question "will the model provider accept it",
// which can only be asked once the schema is known to be valid — and is much
// cheaper to answer before a run than after one.
//
// strict asks for the additional constraints strict decoding imposes. It is
// only checked when the agent asked for strict, because those constraints would
// otherwise reject schemas that are perfectly usable in normal mode.
func CheckPortable(canonical []byte, strict bool) error {
	var document map[string]any
	if err := json.Unmarshal(canonical, &document); err != nil {
		return fmt.Errorf("parse output schema: %w", err)
	}
	if err := walk(document, func(node map[string]any, path string) error {
		if pattern, ok := node["pattern"].(string); ok {
			if err := checkPattern(pattern, path); err != nil {
				return err
			}
		}
		if strict {
			return checkStrictNode(node, path)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// walk visits every schema node, including the ones nested in $defs, items and
// anyOf. A constraint that is only checked at the root is not checked at all.
func walk(node map[string]any, visit func(map[string]any, string) error) error {
	return walkAt(node, "schema", visit)
}

func walkAt(node map[string]any, path string, visit func(map[string]any, string) error) error {
	if err := visit(node, path); err != nil {
		return err
	}
	if defs, ok := node["$defs"].(map[string]any); ok {
		names := make([]string, 0, len(defs))
		for name := range defs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child, _ := defs[name].(map[string]any)
			if child == nil {
				continue
			}
			if err := walkAt(child, path+".$defs."+name, visit); err != nil {
				return err
			}
		}
	}
	if properties, ok := node["properties"].(map[string]any); ok {
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child, _ := properties[name].(map[string]any)
			if child == nil {
				continue
			}
			if err := walkAt(child, path+".properties."+name, visit); err != nil {
				return err
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		if err := walkAt(items, path+".items", visit); err != nil {
			return err
		}
	}
	if anyOf, ok := node["anyOf"].([]any); ok {
		for i, item := range anyOf {
			child, _ := item.(map[string]any)
			if child == nil {
				continue
			}
			if err := walkAt(child, fmt.Sprintf("%s.anyOf[%d]", path, i), visit); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkPattern refuses a regular expression the providers disagree about.
//
// Go compiles with RE2, which is the common denominator; the word-boundary
// assertions some providers' regex engines lack are refused by name rather than
// left to fail at generation time.
func checkPattern(pattern, path string) error {
	if strings.Contains(pattern, `\b`) || strings.Contains(pattern, `\B`) {
		return fmt.Errorf("%s.pattern uses a word boundary (\\b/\\B), which not every model provider supports", path)
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("%s.pattern is not a valid regular expression: %w", path, err)
	}
	program, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return fmt.Errorf("%s.pattern cannot be compiled: %w", path, err)
	}
	if len(program.Inst) > maxPatternInstructions {
		return fmt.Errorf("%s.pattern expands to %d instructions, above the %d limit",
			path, len(program.Inst), maxPatternInstructions)
	}
	return nil
}

// checkStrictNode enforces what strict decoding requires of an object node.
//
// Strict mode is a promise to the provider that every property is always
// present and nothing else ever is; a schema that does not say so is rejected
// by the provider, so it is rejected here instead.
func checkStrictNode(node map[string]any, path string) error {
	properties, ok := node["properties"].(map[string]any)
	if !ok {
		return nil
	}
	if additional, present := node["additionalProperties"]; !present || additional != false {
		return fmt.Errorf("%s must set \"additionalProperties\": false to be usable in strict mode", path)
	}
	required := make(map[string]bool, len(properties))
	if list, ok := node["required"].([]any); ok {
		for _, item := range list {
			if name, ok := item.(string); ok {
				required[name] = true
			}
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !required[name] {
			return fmt.Errorf("%s.required is missing %q; strict mode needs every property required", path, name)
		}
	}
	return nil
}
