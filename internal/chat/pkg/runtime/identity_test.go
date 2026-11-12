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

package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// TestIdentityComesFromTheSender pins which of the two ids a runtime answers
// with. They are not interchangeable: Identity is the tenant-scoped user_id that
// every authorization, record owner and allow-list names, and it is empty for a
// caller this house cannot name; Handle is the platform's own app-scoped id,
// kept because what is bound by handle is looked up by it.
//
// The empty identity is the point. A REST caller has no platform handle at all,
// and a Lark sender from another tenant has no user_id here — in both cases the
// honest answer is "none", not the open_id.
func TestIdentityComesFromTheSender(t *testing.T) {
	const (
		userID = "go.kong"
		openID = "ou_684c62db025820b3af793e43a8b549ee"
	)

	tests := []struct {
		name         string
		dr           *DynamicRuntime
		wantIdentity string
		wantHandle   string
	}{
		{
			name:         "one of ours answers with both ids",
			dr:           &DynamicRuntime{Sender: v1.NewSender(userID, openID)},
			wantIdentity: userID,
			wantHandle:   openID,
		},
		{
			name:         "a sender outside this tenant has no identity",
			dr:           &DynamicRuntime{Sender: v1.NewSender("", openID)},
			wantIdentity: "",
			wantHandle:   openID,
		},
		{
			name:         "a REST or MCP caller has an identity and no handle",
			dr:           &DynamicRuntime{Sender: v1.NewSender(userID, "")},
			wantIdentity: userID,
			wantHandle:   "",
		},
		{
			name:         "a caller with neither is nobody",
			dr:           &DynamicRuntime{},
			wantIdentity: "",
			wantHandle:   "",
		},
		{
			// From(ctx) never returns a typed nil, but the accessors are reachable
			// on one and must not panic: a surface with no runtime is a caller the
			// command tree still has to answer.
			name:         "no runtime at all",
			dr:           nil,
			wantIdentity: "",
			wantHandle:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dr.Identity(); got != tt.wantIdentity {
				t.Errorf("Identity() = %q, want %q", got, tt.wantIdentity)
			}
			if got := tt.dr.Handle(); got != tt.wantHandle {
				t.Errorf("Handle() = %q, want %q", got, tt.wantHandle)
			}
		})
	}
}

// TestAddressIsNilSafeForSurfacesWithNoReceiver pins the second half of the pair.
// An MCP or TUI runtime carries no receiver at all, so reading the address off one
// is a nil dereference — the accessor is what makes "this call cannot be replied
// to" an answer rather than a crash, and it is why callers must not reach past it.
func TestAddressIsNilSafeForSurfacesWithNoReceiver(t *testing.T) {
	noReceiver := &DynamicRuntime{Source: RequestSourceMCP}

	if got := noReceiver.Address(); got != "" {
		t.Errorf("Address() = %q, want empty (an MCP caller has nowhere to be replied to)", got)
	}
	if noReceiver.IsP2P() || noReceiver.IsGroup() {
		t.Error("a call with no receiver claims to come from a conversation")
	}
	if got := noReceiver.ParentMessageID(); got != "" {
		t.Errorf("ParentMessageID() = %q, want empty", got)
	}
	if (*DynamicRuntime)(nil).Address() != "" {
		t.Error("Address() on a nil runtime must answer, not panic")
	}
}

// TestIdentityAndAddressHaveOneCarrier is an architecture test: it reads the
// source rather than the behaviour, because the compiler cannot see the rule it
// enforces.
//
// The rule is that the caller's identity, the reply's address and the platform's
// handle each have one way to be reached — Identity(), Address(), Handle() — and
// that the receiver is an implementation detail of the runtime rather than
// something every caller reads fields off. Both halves are load-bearing:
//
//   - A second home for the identity is how the message path and the card path
//     came to disagree about the same person. Receiver had one (sender_id, since
//     retired from the proto), and for a direct message it held the same string
//     as the address — which is what let the two be read as one value whose
//     absence broke both.
//   - Reading the receiver's fields directly is how an MCP or TUI call, whose
//     runtime carries no receiver at all, came to dereference nil where the
//     accessor would have answered "there is nowhere to reply".
//
// It is written as a source scan rather than a comment because the last time
// this invariant was only a comment, it grew back.
func TestIdentityAndAddressHaveOneCarrier(t *testing.T) {
	root := moduleRoot(t)
	var findings []string

	for _, dir := range []string{"internal/chat", "pkg/api/chat"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".pb.go") || strings.HasSuffix(name, ".pb.defaults.go") {
				return nil
			}
			// The runtime package owns the receiver; reaching into it is its job.
			if strings.Contains(path, filepath.Join("internal", "chat", "pkg", "runtime")) {
				return nil
			}
			// A test builds its input, so it may fill a receiver in whole; what it
			// must not do either is read the identity or the address off one.
			strict := !strings.HasSuffix(name, "_test.go")
			findings = append(findings, scanIdentityCarriers(t, path, strict)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}

	if len(findings) > 0 {
		t.Errorf("the caller's identity or the reply's address is read from a second place:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

// scanIdentityCarriers reports every construct in one file that reaches past the
// single accessor for who the caller is or where the reply goes. strict extends
// the receiver rule from the identity/address pair to every field, which is what
// production code has to satisfy.
func scanIdentityCarriers(t *testing.T, path string, strict bool) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	rel, err := filepath.Rel(moduleRoot(t), path)
	if err != nil {
		rel = path
	}

	var findings []string
	at := func(pos token.Pos) string {
		return rel + ":" + strconv.Itoa(fset.Position(pos).Line)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			// A literal that sets sender_id is a second place the identity is
			// written. The constructors in pkg/api/chat/v1 deliberately leave it
			// unset, and no literal anywhere should put it back.
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "SenderId" {
					findings = append(findings, at(key.Pos())+": sets SenderId on a receiver literal")
				}
			}
		case *ast.SelectorExpr:
			inner, ok := node.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "Receiver" {
				return true
			}
			identityOrAddress := node.Sel.Name == "Id" || node.Sel.Name == "SenderId"
			if !identityOrAddress && !strict {
				return true
			}
			findings = append(findings, at(node.Sel.Pos())+": reads .Receiver."+node.Sel.Name+
				" (the runtime has an accessor for it)")
		}
		return true
	})
	return findings
}

// moduleRoot walks up from the test's directory to the module root, so the scan
// covers the same tree however the test is run.
func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}
