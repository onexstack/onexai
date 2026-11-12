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

package pkg

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoInternalImports is the guard on the public tree's boundary: pkg/ is
// what an external client imports, so nothing under it may depend on
// internal/, which that client can never see or use.
//
// The rule is structural rather than stylistic. A package that imports
// internal/ still compiles inside this module, so the violation is invisible
// until someone outside the module tries to use the public package — by then
// the fix is to move types across a boundary nobody was watching. This test
// makes the boundary a build failure instead.
func TestNoInternalImports(t *testing.T) {
	const prefix = "github.com/onexstack/onexai/internal/"

	var violations []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		// ImportsOnly: the guard is about the import graph, so the rest of each
		// file need not parse — and a file that does not parse should not make
		// this test the one that reports it.
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range f.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", path, err)
			}
			if strings.HasPrefix(importPath, prefix) {
				violations = append(violations, path+" imports "+importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk pkg: %v", err)
	}

	if len(violations) > 0 {
		t.Fatalf("pkg/ must not import internal/:\n  %s", strings.Join(violations, "\n  "))
	}
}
