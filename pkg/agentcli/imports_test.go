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

package agentcli

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestAgentCLIImportsNothingInternal is the guard on this package's reason for
// existing: it is the one tree both the private harness CLI and cmd/oai-chat-apiserver compile
// against, so it must reach into neither service's packages.
//
// The coupling it prevents is not hypothetical. The two trees currently
// reference each other nowhere, and this package is what would first connect
// them: the day it imports a chat type, the CLI starts compiling a
// service it does not run, and the reverse import — which the chat
// genuinely wants, since it drives this runtime — becomes an import cycle. A
// cycle is a build failure rather than a design slip, but by then the fix is to
// move types across a boundary nobody was watching.
//
// Imports of pkg (the shared tree this package belongs to) are fine,
// and so is anything outside the module. Test files are scanned too: a test that
// reaches into a service tree makes the shared package's own test binary depend
// on that service.
func TestAgentCLIImportsNothingInternal(t *testing.T) {
	const module = "github.com/onexstack/onexai/internal/"
	forbidden := []string{module + "chat", module + "onexai"}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		scanned++

		// ImportsOnly: the guard is about the import graph, so the rest of each
		// file need not parse — and a file that does not parse should not make
		// this test the one that reports it.
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", entry.Name(), err)
			continue
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("%s: unquote %s: %v", entry.Name(), spec.Path.Value, err)
				continue
			}
			for _, bad := range forbidden {
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s imports %s, which is one of the two service trees this package is shared between",
						entry.Name(), path)
				}
			}
		}
	}

	if scanned == 0 {
		t.Fatal("no Go files found; the scan is not looking at the real package")
	}
}
