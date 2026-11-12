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

package commandreg

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/generated/clientset/versioned/fake"
)

func TestSyncCreatesEveryCatalogEntry(t *testing.T) {
	client := fake.NewSimpleClientset()
	api := client.ChatV1alpha1().Commands("default")

	desired := desiredCommands()
	if err := syncCommands(context.Background(), api, desired); err != nil {
		t.Fatalf("syncCommands: %v", err)
	}

	list, err := api.List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != len(desired) {
		t.Fatalf("created %d commands, want %d", len(list.Items), len(desired))
	}

	byName := make(map[string]chatv1alpha1.Command, len(list.Items))
	for _, item := range list.Items {
		byName[item.Name] = item
	}
	for _, want := range desired {
		got, ok := byName[want.Name]
		if !ok {
			t.Fatalf("command %q was not created", want.Name)
		}
		if got.Spec.Group != want.Spec.Group {
			t.Errorf("%s: group = %q, want %q", want.Name, got.Spec.Group, want.Spec.Group)
		}
		if got.Spec.Source != SourceBuiltin {
			t.Errorf("%s: source = %q, want %q", want.Name, got.Spec.Source, SourceBuiltin)
		}
		// The console's command picker shows the description next to the
		// name; an empty one silently degrades the picker to bare names.
		if got.Spec.Description == "" {
			t.Errorf("%s: description is empty", want.Name)
		}
		if got.Spec.Description != want.Spec.Description {
			t.Errorf("%s: description = %q, want %q",
				want.Name, got.Spec.Description, want.Spec.Description)
		}
		if got.Labels[ManagedByLabel] != ManagedByValue {
			t.Errorf("%s: missing the managed-by label", want.Name)
		}
		if got.Labels["agent.onexstack.io/group"] != want.Spec.Group {
			t.Errorf("%s: group label does not match spec.group", want.Name)
		}
	}
}

func TestSyncPrunesOnlyManagedCommands(t *testing.T) {
	stale := &chatv1alpha1.Command{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "removed-command",
			Namespace: "default",
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
		Spec: chatv1alpha1.CommandSpec{Group: "core", Source: SourceBuiltin},
	}
	operatorOwned := &chatv1alpha1.Command{
		ObjectMeta: metav1.ObjectMeta{Name: "operator-command", Namespace: "default"},
		Spec:       chatv1alpha1.CommandSpec{Group: "core", Source: "custom"},
	}
	client := fake.NewSimpleClientset(stale, operatorOwned)
	api := client.ChatV1alpha1().Commands("default")

	if err := syncCommands(context.Background(), api, desiredCommands()); err != nil {
		t.Fatalf("syncCommands: %v", err)
	}

	if _, err := api.Get(context.Background(), "removed-command", metav1.GetOptions{}); err == nil {
		t.Error("a managed command the binary no longer implements must be deleted")
	}
	if _, err := api.Get(context.Background(), "operator-command", metav1.GetOptions{}); err != nil {
		t.Errorf("an unmanaged command must survive the prune: %v", err)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	api := client.ChatV1alpha1().Commands("default")

	if err := syncCommands(context.Background(), api, desiredCommands()); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	client.ClearActions()

	if err := syncCommands(context.Background(), api, desiredCommands()); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	for _, action := range client.Actions() {
		switch action.GetVerb() {
		case "create", "update", "patch", "delete":
			t.Errorf("second sync wrote %q on %s; it must be a no-op", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestDesiredCommandsCarryTheCatalogueLabels(t *testing.T) {
	for _, cmd := range desiredCommands() {
		if cmd.Name == "" || strings.TrimSpace(cmd.Name) != cmd.Name {
			t.Fatalf("a command has an invalid name: %q", cmd.Name)
		}
		if cmd.Spec.Group == "" {
			t.Fatalf("%s: a command must name its group", cmd.Name)
		}
	}
}

// TestNamespacesToPublishCoversEveryBinding pins the reason the catalogue is
// written more than once: a binding resolves its commands inside its own
// namespace, so a namespace whose bindings are not published into grants
// nothing — and does it without an error.
func TestNamespacesToPublishCoversEveryBinding(t *testing.T) {
	client := fake.NewSimpleClientset(
		&agentv1alpha1.AgentBinding{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "onex"}},
		&agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "helper-public"}},
	)

	got, err := namespacesToPublish(context.Background(), "",
		client.AgentV1alpha1().AgentBindings(metav1.NamespaceAll),
		client.AgentV1alpha1().Agents(metav1.NamespaceAll))
	if err != nil {
		t.Fatalf("namespacesToPublish: %v", err)
	}

	want := []string{DefaultNamespace, "helper-public", "onex"}
	if len(got) != len(want) {
		t.Fatalf("namespaces = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("namespaces = %v, want %v", got, want)
		}
	}
}
