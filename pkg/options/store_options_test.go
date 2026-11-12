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

package options

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestBackendForDefaultsToTheControlPlane(t *testing.T) {
	o := NewStoreOptions()
	if got := o.BackendFor("Agent"); got != BackendControlPlane {
		t.Fatalf("backend = %q, want %q", got, BackendControlPlane)
	}

	o.ReadBackend = BackendMirrorList
	if got := o.BackendFor("Agent"); got != BackendMirrorList {
		t.Fatalf("backend = %q, want the global default", got)
	}

	o.ReadBackends = map[string]string{"AuthorizationRule": BackendControlPlane}
	if got := o.BackendFor("AuthorizationRule"); got != BackendControlPlane {
		t.Fatalf("per-kind override was ignored: %q", got)
	}
	if got := o.BackendFor("Agent"); got != BackendMirrorList {
		t.Fatalf("an override leaked to another kind: %q", got)
	}
	if !o.UsesMirror() {
		t.Fatal("UsesMirror did not see the global mirror backend")
	}
}

func TestBackendForMatchesLowercasedConfigKeys(t *testing.T) {
	// Viper lower-cases map keys, so `MCPServer: mirror-list` in YAML arrives as
	// `mcpserver`. The kind lookup must still find it, and validation must not
	// call it an unknown kind.
	o := NewStoreOptions()
	o.ReadBackends = map[string]string{"mcpserver": BackendMirrorList}

	if got := o.BackendFor("MCPServer"); got != BackendMirrorList {
		t.Fatalf("backend = %q, want %q", got, BackendMirrorList)
	}
	if errs := o.Validate(); len(errs) != 0 {
		t.Fatalf("a lower-cased kind was rejected: %v", errs)
	}
	if kinds := o.MirroredKinds(); len(kinds) != 1 || kinds[0] != "MCPServer" {
		t.Fatalf("mirrored kinds = %v, want [MCPServer]", kinds)
	}
}

func TestValidateAcceptsTheDefaults(t *testing.T) {
	o := NewStoreOptions()
	o.Source = "sql"
	if errs := o.Validate(); len(errs) != 0 {
		t.Fatalf("defaults were rejected: %v", errs)
	}
}

func TestValidateRejectsAnUnknownKind(t *testing.T) {
	o := NewStoreOptions()
	o.ReadBackends = map[string]string{"Agnets": BackendMirrorList}
	if errs := o.Validate(); len(errs) == 0 || !strings.Contains(errs[0].Error(), "Agnets") {
		t.Fatalf("a misspelt kind was accepted: %v", errs)
	}
}

func TestValidateAcceptsCredentialBearingKinds(t *testing.T) {
	// Model and MCPServer rows carry credentials that live in a Secret. They are
	// allowed here because the projection's decoder resolves that Secret on the
	// way out, exactly as the lister path does.
	for _, kind := range []string{"Model", "MCPServer"} {
		o := NewStoreOptions()
		o.ReadBackends = map[string]string{kind: BackendMirrorList}
		if errs := o.Validate(); len(errs) != 0 {
			t.Fatalf("%s was rejected: %v", kind, errs)
		}
	}
}

func TestValidateAcceptsTheFullBackend(t *testing.T) {
	o := NewStoreOptions()
	o.ReadBackends = map[string]string{"Agent": BackendMirrorFull}
	if errs := o.Validate(); len(errs) != 0 {
		t.Fatalf("mirror-full was rejected: %v", errs)
	}
	if kinds := o.InformerlessKinds(); len(kinds) != 1 || kinds[0] != "Agent" {
		t.Fatalf("informerless kinds = %v, want [Agent]", kinds)
	}
	if o.InformerFilter()(schema.GroupVersionKind{Group: "agent.onexai.io", Kind: "Agent"}) {
		t.Fatal("the filter kept the informer of a mirror-full kind")
	}
	if !o.InformerFilter()(schema.GroupVersionKind{Group: "agent.onexai.io", Kind: "Prompt"}) {
		t.Fatal("the filter dropped the informer of a controlplane kind")
	}
}
