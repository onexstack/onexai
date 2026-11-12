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

package kubelistcheck

import (
	"go/ast"
	"go/parser"
	"testing"
)

// The directive is a list, and the linter this package implements is rarely the
// first one named. A substring search would work only while that stayed true.
func TestNolintNames(t *testing.T) {
	tests := []struct {
		comment string
		want    bool
	}{
		{"//nolint:kubelistcheck", true},
		{"//nolint:exhaustruct,kubelistcheck // and a reason", true},
		{"//nolint:kubelistcheck,exhaustruct", true},
		{"//nolint:kubelistcheck // a reason", true},
		{"//nolint:exhaustruct", false},
		{"//nolint:kubelistchecker", false},
		{"// just a comment", false},
		{"//nolint", false},
	}

	for _, tc := range tests {
		if got := nolintNames(tc.comment, "kubelistcheck"); got != tc.want {
			t.Errorf("nolintNames(%q) = %v, want %v", tc.comment, got, tc.want)
		}
	}
}

func TestHasAnyField(t *testing.T) {
	tests := []struct {
		src  string
		want bool
	}{
		{`metav1.ListOptions{ResourceVersion: "0"}`, true},
		{`metav1.ListOptions{Continue: token}`, true},
		{`metav1.ListOptions{LabelSelector: "a=b"}`, false},
		{`metav1.ListOptions{}`, false},
	}

	for _, tc := range tests {
		expr, err := parser.ParseExpr(tc.src)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.src, err)
		}

		lit, ok := expr.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("%q is not a composite literal", tc.src)
		}

		if got := hasAnyField(lit, cacheFields); got != tc.want {
			t.Errorf("hasAnyField(%s) = %v, want %v", tc.src, got, tc.want)
		}
	}
}

func TestExprName(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{`ListOptions{}`, "ListOptions"},
		{`metav1.ListOptions{}`, "ListOptions"},
	}

	for _, tc := range tests {
		expr, err := parser.ParseExpr(tc.src)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.src, err)
		}

		if got := exprName(expr.(*ast.CompositeLit).Type); got != tc.want {
			t.Errorf("exprName(%s) = %q, want %q", tc.src, got, tc.want)
		}
	}
}
