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

// Package openapi exposes the OpenAPI definitions this API server serves.
//
// The generic apiserver refuses to start unless every served resource has a
// model: the built-in core/coordination/rbac groups, the apiextensions types the
// CRD sub-server serves, and the project's own resources. openapi-gen only
// produces the last group, so the built-in definitions are merged in here. This
// file is hand-maintained on purpose: zz_generated.openapi.go is rewritten by
// 'make gen-k8s' and must not be edited.
package openapi

import (
	"strings"
	"unicode"

	common "k8s.io/kube-openapi/pkg/common"
	kubeopenapi "k8s.io/kubernetes/pkg/generated/openapi"
)

// GetOpenAPIDefinitionsWithBuiltin returns the built-in Kubernetes definitions
// with this project's definitions layered on top.
func GetOpenAPIDefinitionsWithBuiltin(ref common.ReferenceCallback) map[string]common.OpenAPIDefinition {
	builtin := kubeopenapi.GetOpenAPIDefinitions(ref)

	definitions := make(map[string]common.OpenAPIDefinition, len(builtin)*2)
	for name, definition := range builtin {
		definitions[name] = definition
		// openapi-gen resolves a referenced dependency by its Go type name
		// (k8s.io/apimachinery/pkg/apis/meta/v1.ObjectMeta), while the built-in
		// table is keyed by OpenAPI model name. Register both spellings, or
		// every schema that embeds metav1.ObjectMeta fails to build.
		if alias, ok := goTypeName(name); ok {
			definitions[alias] = definition
		}
	}

	for name, definition := range GetOpenAPIDefinitions(ref) {
		definitions[name] = definition
	}
	return definitions
}

// goTypeName converts an OpenAPI model name such as
// io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta into the Go type name
// k8s.io/apimachinery/pkg/apis/meta/v1.ObjectMeta. Kubernetes model names mirror
// their import paths, so the mapping is mechanical; anything that does not start
// with io.k8s. is not a built-in and is left alone.
func goTypeName(model string) (string, bool) {
	parts := strings.Split(model, ".")
	if len(parts) < 4 || parts[0] != "io" || parts[1] != "k8s" {
		return "", false
	}
	typeName := parts[len(parts)-1]
	if typeName == "" || !unicode.IsUpper(rune(typeName[0])) {
		return "", false
	}
	pkg := strings.Join(parts[2:len(parts)-1], "/")
	if pkg == "" {
		return "", false
	}
	return "k8s.io/" + pkg + "." + typeName, true
}
