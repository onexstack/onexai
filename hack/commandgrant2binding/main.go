// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// commandgrant2binding converts CommandGrant objects into AgentBindings.
//
// CommandGrant is gone: a command grant is an AgentBinding. The binding can now
// hold the same two axes the grant did — named commands and a label selector
// (spec.commandSelector) — but this tool still expands the selector into names
// against the compiled catalogue. A migrated object should mean exactly what the
// grant meant on the day it was migrated; a grant that should follow the
// catalogue can be rewritten as a selector afterwards. A selector that matched
// nothing (a typo, or a group that never existed) expands to nothing, which
// preserves the old grant's actual effect.
//
// Usage:
//
//	kubectl --kubeconfig ~/.onexai/declserver.kubeconfig get commandgrants -A -o yaml \
//	  > /tmp/commandgrants.yaml
//	go run ./hack/commandgrant2binding /tmp/commandgrants.yaml > /tmp/agentbindings.yaml
//	kubectl --kubeconfig ~/.onexai/declserver.kubeconfig apply -f /tmp/agentbindings.yaml
//
// Run it before the control plane stops serving CommandGrant: once the type is
// removed, the objects are no longer readable through the API.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/onexstack/onexai/internal/chat/pkg/command"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: commandgrant2binding <commandgrants.yaml>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	objects, err := decode(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	for _, obj := range objects {
		if obj.GetKind() != "CommandGrant" {
			continue
		}
		binding, err := convert(obj)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s/%s: %v\n", obj.GetNamespace(), obj.GetName(), err)
			continue
		}
		out, err := yaml.Marshal(binding)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("---")
		os.Stdout.Write(out)
	}
}

// convert builds one AgentBinding from one CommandGrant.
func convert(obj *unstructured.Unstructured) (map[string]any, error) {
	spec, ok := obj.Object["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("no spec")
	}

	commands := stringList(spec["commands"])
	expanded, err := expand(stringValue(spec["commandSelector"]))
	if err != nil {
		return nil, fmt.Errorf("commandSelector: %w", err)
	}
	commands = merge(commands, expanded)

	outSpec := map[string]any{
		"enabled": boolValue(spec["enabled"], true),
	}
	agents := stringList(spec["agentNames"])
	if len(agents) > 0 {
		outSpec["agents"] = agents
	}
	if len(commands) > 0 {
		outSpec["commands"] = commands
	}
	if selector, ok := spec["agentSelector"].(map[string]any); ok {
		// It is already the API shape; a JSON round-trip drops nothing.
		out := map[string]any{}
		raw, _ := json.Marshal(selector)
		_ = json.Unmarshal(raw, &out)
		outSpec["selector"] = out
	}
	for _, key := range []string{"displayName", "description"} {
		if value := stringValue(spec[key]); value != "" {
			outSpec[key] = value
		}
	}

	if _, hasSelector := outSpec["selector"]; !hasSelector && len(agents) == 0 {
		return nil, fmt.Errorf("the grant names and selects no agent")
	}

	return map[string]any{
		"apiVersion": "agent.onexai.io/v1alpha1",
		"kind":       "AgentBinding",
		"metadata": map[string]any{
			"name":      obj.GetName(),
			"namespace": obj.GetNamespace(),
		},
		"spec": outSpec,
	}, nil
}

// expand turns a command selector into the catalogue names it matches.
func expand(selector string) ([]string, error) {
	if selector == "" {
		return nil, nil
	}
	parsed, err := labels.Parse(selector)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, set := range command.AllLabels() {
		if parsed.Matches(set) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// decode accepts either a single object or a List.
func decode(data []byte) ([]*unstructured.Unstructured, error) {
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []*unstructured.Unstructured
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if len(obj.Object) == 0 {
			continue
		}
		if obj.GetKind() == "List" {
			items, _, err := unstructured.NestedSlice(obj.Object, "items")
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				raw, ok := item.(map[string]any)
				if !ok {
					continue
				}
				out = append(out, &unstructured.Unstructured{Object: raw})
			}
			continue
		}
		out = append(out, &obj)
	}
	return out, nil
}

func stringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any, fallback bool) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return fallback
}

func merge(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, item := range list {
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
