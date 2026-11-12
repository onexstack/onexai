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

package agentlabel

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"

	agenttypes "github.com/onexstack/onexai/internal/agent/model"
)

// TestDefaultNamespaceMatchesAgentTypes pins the duplicated constant.
//
// Chat talks to the agent service over its API, so it does not import the
// agent's packages — which makes this one literal a copy, and a copy that
// drifts is how an agent with no namespace set comes to be selected by rules
// written for a namespace it does not have.
func TestDefaultNamespaceMatchesAgentTypes(t *testing.T) {
	if DefaultNamespace != agenttypes.DefaultNamespace {
		t.Errorf("DefaultNamespace = %q, agent types say %q", DefaultNamespace, agenttypes.DefaultNamespace)
	}
}

// TestFields covers the field space: the record's own columns, and the default.
func TestFields(t *testing.T) {
	got := Fields("bke-agent", "")
	if got[FieldName] != "bke-agent" || got[FieldNamespace] != DefaultNamespace {
		t.Errorf("Fields = %v, want the name and the default workspace", got)
	}

	got = Fields("bke-agent", "bke")
	if got[FieldNamespace] != "bke" {
		t.Errorf("workspace = %q, want the stated one", got[FieldNamespace])
	}
}

// TestFields_AreNotLabels pins the property the whole split exists for: a rule
// written in one space cannot see the other.
//
// It is the reason no name has to be reserved. A label called "workspace" and the
// record's workspace are different namespaces, so neither can shadow the other
// — a label selector over the tag can never be satisfied by the record, and the
// field selector over the record never sees the tag.
func TestFields_AreNotLabels(t *testing.T) {
	ls := labels.Set{"workspace": "bke"}
	fs := Fields("bke-agent", "helper-public")

	labelSel, err := labels.Parse("workspace=bke")
	if err != nil {
		t.Fatalf("labels.Parse = %v", err)
	}
	if !labelSel.Matches(ls) {
		t.Error("the label selector did not match the tag")
	}
	if labelSel.Matches(labels.Set(fs)) {
		t.Error("a label selector matched the field set: the two spaces are not separate")
	}

	fieldSel, err := fields.ParseSelector("metadata.workspace=helper-public")
	if err != nil {
		t.Fatalf("fields.ParseSelector = %v", err)
	}
	if !fieldSel.Matches(fs) {
		t.Error("the field selector did not match the record")
	}
	if fieldSel.Matches(fields.Set(ls)) {
		t.Error("a field selector matched the label set: the two spaces are not separate")
	}
}

// TestFieldNames pins that the closed set is closed and usable.
//
// The set is what makes a mistyped field refusable at write time; an empty one
// would make every field selector refusable, and a name missing from it would
// make the corresponding selector impossible to write.
func TestFieldNames(t *testing.T) {
	names := FieldNames()
	// Three during the rename: the name, the field, and the field's deprecated
	// spelling, which selectors written before the rename still use.
	if len(names) != 3 {
		t.Fatalf("FieldNames = %v, want the name, the namespace and its legacy spelling", names)
	}
	for _, want := range []string{FieldName, FieldNamespace, LegacyFieldWorkspace} {
		if !slices.Contains(names, want) {
			t.Errorf("FieldNames = %v, missing %q", names, want)
		}
	}
	for _, name := range names {
		if _, err := fields.ParseSelector(name + "=x"); err != nil {
			t.Errorf("FieldNames offers %q, which is not a usable field name: %v", name, err)
		}
	}
}

// TestPrefix pins that the prefix produces keys a selector can actually use.
//
// It is a convention rather than a namespace this package enforces — a bare tag
// is matched too — so the one thing worth checking is that following it is
// possible.
func TestPrefix(t *testing.T) {
	if _, err := labels.Parse(Prefix + "onex"); err != nil {
		t.Errorf("Prefix %q does not produce usable label keys: %v", Prefix, err)
	}
}
