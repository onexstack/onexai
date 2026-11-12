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

// Package commandreg publishes the compiled command catalogue to the control
// plane.
//
// A command's body is code: it is compiled into this binary and cannot be
// created or edited as an object. The Command objects this package reconciles
// are its catalogue entries — the names an AgentBinding may grant, the groups
// and labels an operator selects on. One object per command, created at start-up
// and kept in step with the binary, is what makes the grant list reviewable with
// kubectl instead of only through a running process.
//
// The catalogue is published into every namespace that has an agent or a
// binding, not just into this deployment's own. A binding resolves the commands
// it grants inside its own namespace (internal/controller/agent/agentbinding
// resolve.go lists Command in binding.Namespace), and bindings live in the
// namespace they select agents from. Publishing only into the configured
// namespace therefore leaves every binding outside it granting commands that
// "do not exist" — the grant is silently dropped and the agent never becomes
// ready.
package commandreg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	"github.com/onexstack/onexai/internal/chat/pkg/command"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

const (
	// ManagedByLabel marks the objects this package owns. Pruning only ever
	// touches objects carrying it, so an operator's own Command object — however
	// unlikely — is never deleted by a binary upgrade.
	ManagedByLabel = "onexai.io/managed-by"
	// ManagedByValue is this package's identity in that label.
	ManagedByValue = "oai-chat-apiserver"
	// SourceBuiltin is spec.source for a command compiled into this binary.
	SourceBuiltin = "builtin"
	// DefaultNamespace is used when the control-plane client was built without
	// a namespace: objects must live somewhere, and the bare namespace every
	// deployment has is the only defensible guess.
	DefaultNamespace = "default"
	// SyncInterval bounds how long a hand-deleted Command object can stay gone.
	// The sync is cheap and idempotent, so the period is a self-healing bound
	// rather than a load knob.
	SyncInterval = 10 * time.Minute
)

// RunOnce brings the control plane in line with the compiled catalogue, in
// every namespace a binding could resolve it from.
func RunOnce(ctx context.Context, client *declclient.Client, namespace string) error {
	if client == nil {
		return nil
	}
	targets, err := targetNamespaces(ctx, client, namespace)
	if err != nil {
		return err
	}

	desired := desiredCommands()
	var errs []error
	for _, ns := range targets {
		api := client.Clientset.ChatV1alpha1().Commands(ns)
		if err := syncCommands(ctx, api, desired); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", ns, err))
		}
	}
	return errors.Join(errs...)
}

// targetNamespaces is where a catalogue entry can be needed: this deployment's
// own namespace, plus every namespace that holds an agent or a binding.
//
// Listing rather than deriving from the informer cache is deliberate — the
// cache is scoped to one namespace when controlplane.namespace is set, which is
// exactly the case this has to see past, and the call is off the request path.
func targetNamespaces(ctx context.Context, client *declclient.Client, namespace string) ([]string, error) {
	return namespacesToPublish(ctx, namespace,
		client.Clientset.AgentV1alpha1().AgentBindings(metav1.NamespaceAll),
		client.Clientset.AgentV1alpha1().Agents(metav1.NamespaceAll))
}

func namespacesToPublish(ctx context.Context, namespace string, bindings agentBindingLister, agents agentLister) ([]string, error) {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	set := map[string]struct{}{namespace: {}}

	// The informer cache is scoped to one namespace when controlplane.namespace
	// is set, which is exactly the case this has to see past.
	bindingList, err := bindings.List(ctx, metav1.ListOptions{}) //nolint:kubelistcheck // must see namespaces outside the informer's scope
	if err != nil {
		return nil, fmt.Errorf("list agent bindings: %w", err)
	}
	for i := range bindingList.Items {
		if ns := bindingList.Items[i].Namespace; ns != "" {
			set[ns] = struct{}{}
		}
	}

	agentList, err := agents.List(ctx, metav1.ListOptions{}) //nolint:kubelistcheck // must see namespaces outside the informer's scope
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	for i := range agentList.Items {
		if ns := agentList.Items[i].Namespace; ns != "" {
			set[ns] = struct{}{}
		}
	}

	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out, nil
}

// agentBindingLister and agentLister are the cluster-wide listings this package
// needs; they are interfaces so a fake can stand in for the API server.
type agentBindingLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*agentv1alpha1.AgentBindingList, error)
}

type agentLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*agentv1alpha1.AgentList, error)
}

// syncCommands is RunOnce without the client plumbing, so the tests can run it
// against a fake.
func syncCommands(ctx context.Context, api commandClient, desired []*chatv1alpha1.Command) error {
	var errs []error
	for _, want := range desired {
		if err := upsert(ctx, api, want); err != nil {
			errs = append(errs, fmt.Errorf("command %q: %w", want.Name, err))
		}
	}
	if err := prune(ctx, api, desired); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Run keeps the catalogue in step for the life of the process.
func Run(ctx context.Context, client *declclient.Client, namespace string) {
	if client == nil {
		return
	}
	sync := func() {
		if err := RunOnce(ctx, client, namespace); err != nil {
			// A failure here does not remove anything: the previous round's
			// objects stay, and the next round repairs them.
			slog.WarnContext(ctx, "command catalogue: reconcile failed", "error", err)
			return
		}
		slog.InfoContext(ctx, "command catalogue: reconciled", "commands", len(desiredCommands()))
	}

	sync()

	ticker := time.NewTicker(SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sync()
		}
	}
}

// desiredCommands projects the compiled catalogue into objects.
func desiredCommands() []*chatv1alpha1.Command {
	allLabels := command.AllLabels()

	out := make([]*chatv1alpha1.Command, 0)
	for _, group := range command.Groups() {
		for _, entry := range group.Entries {
			entryLabels := allLabels[entry.Name]
			objectLabels := make(map[string]string, len(entryLabels)+1)
			for key, value := range entryLabels {
				objectLabels[key] = value
			}
			objectLabels[ManagedByLabel] = ManagedByValue

			out = append(out, &chatv1alpha1.Command{
				ObjectMeta: metav1.ObjectMeta{
					Name:   entry.Name,
					Labels: objectLabels,
				},
				Spec: chatv1alpha1.CommandSpec{
					CommonSpec: chatv1alpha1.CommonSpec{
						Enabled:     true,
						DisplayName: entry.Name,
						// The console's command picker shows this beside the
						// name; without it an operator granting commands sees
						// only opaque names.
						Description: entry.Short,
					},
					Group:        group.Key,
					Source:       SourceBuiltin,
					AccountLevel: entry.AccountLevel,
				},
			})
		}
	}
	return out
}

// upsert creates the object, or updates the fields this package owns when they
// have drifted. It uses retry-on-conflict because the periodic sync and the
// start-up sync can overlap during a hand-off.
func upsert(ctx context.Context, api commandClient, want *chatv1alpha1.Command) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := api.Get(ctx, want.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err := api.Create(ctx, want.DeepCopy(), metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}

		updated := current.DeepCopy()
		updated.Labels = want.Labels
		updated.Spec = want.Spec
		if apiequality.Semantic.DeepEqual(current.Labels, updated.Labels) &&
			apiequality.Semantic.DeepEqual(current.Spec, updated.Spec) {
			return nil
		}
		_, err = api.Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
}

// prune deletes objects this package created that the binary no longer
// implements: a command removed from the catalogue must stop being grantable.
func prune(ctx context.Context, api commandClient, desired []*chatv1alpha1.Command) error {
	selector := labels.SelectorFromSet(labels.Set{ManagedByLabel: ManagedByValue}).String()
	// A consistent read: this decides which objects to delete, and a cache that
	// lagged a moment would leave a command the binary no longer implements
	// grantable.
	list, err := api.List(ctx, metav1.ListOptions{LabelSelector: selector}) //nolint:kubelistcheck // must not read a stale cache
	if err != nil {
		return fmt.Errorf("list managed commands: %w", err)
	}

	wanted := make(map[string]struct{}, len(desired))
	for _, obj := range desired {
		wanted[obj.Name] = struct{}{}
	}

	for i := range list.Items {
		item := &list.Items[i]
		// The selector should already have excluded anything not ours; checking
		// the label again means a client or proxy that drops the selector cannot
		// turn this into "delete every Command object in the namespace".
		if item.Labels[ManagedByLabel] != ManagedByValue {
			continue
		}
		if _, ok := wanted[item.Name]; ok {
			continue
		}
		if err := api.Delete(ctx, item.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale command %q: %w", item.Name, err)
		}
	}
	return nil
}

// commandClient is the slice of the typed client this package uses. It is an
// interface so the tests can hand in a fake.
type commandClient interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*chatv1alpha1.Command, error)
	Create(ctx context.Context, obj *chatv1alpha1.Command, opts metav1.CreateOptions) (*chatv1alpha1.Command, error)
	Update(ctx context.Context, obj *chatv1alpha1.Command, opts metav1.UpdateOptions) (*chatv1alpha1.Command, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	List(ctx context.Context, opts metav1.ListOptions) (*chatv1alpha1.CommandList, error)
}
