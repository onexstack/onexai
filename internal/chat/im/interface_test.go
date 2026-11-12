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

package im

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestNoDeadStreamTypes guards against the streaming abstraction coming back.
//
// StreamHandle and StreamOptions described a platform-neutral streaming message
// that nothing ever implemented: the bot path streams through the Lark
// streaming-card code directly, and the types had no user. They were removed for
// that reason. The hazard is that they look like the right place to put a new
// streaming feature — a type with a plausible name and no callers reads as an
// extension point rather than as dead code.
//
// A Go test cannot assert that a symbol does not exist, so this reads the
// package's own source. Files are scanned, not just the one that holds the
// declarations, so redeclaring the types anywhere in the package fails here
// instead of passing in silence.
func TestNoDeadStreamTypes(t *testing.T) {
	declared := typeNames(t)
	for _, dead := range []string{"StreamHandle", "StreamOptions"} {
		if declared[dead] {
			t.Errorf("im.%s is declared again: it had no implementation and no callers when it was removed;"+
				" stream through the sink in pkg/stream instead of reviving it", dead)
		}
	}

	// The scan has to be able to see declarations at all, or a broken path would
	// make this test pass by finding nothing.
	if !declared["CardAction"] {
		t.Fatal("the scan found no declaration it knows exists, so it is not reading the package")
	}
}

// typeNames returns the type names declared in the Go files of the package the
// test runs in.
//
// Files are read one at a time rather than with parser.ParseDir, which ignores
// build constraints: this package has none, and a file that is not part of the
// build cannot declare anything that compiles.
func typeNames(t *testing.T) map[string]bool {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	fset := token.NewFileSet()
	names := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					names[ts.Name.Name] = true
				}
			}
		}
	}
	return names
}
