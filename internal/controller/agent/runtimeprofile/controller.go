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

// Package runtimeprofile resolves a RuntimeProfile to the endpoint the gateway
// dials. It is deliberately small: the profile owns the contract, the hosting
// owns the deployment, and this controller only joins them.
package runtimeprofile

import (
	"context"
	"fmt"
	"net/url"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/onexstack/onexai/internal/controller/support"
	"github.com/onexstack/onexai/internal/pkg/clientconfig"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "runtimeprofile-controller"

// Reconciler resolves RuntimeProfile objects.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// Add wires the reconciler into the manager.
func Add(ctx context.Context, mgr ctrl.Manager, options controller.Options) (bool, error) {
	r := &Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor(ControllerName),
	}
	if err := r.SetupWithManager(ctx, mgr, options); err != nil {
		return false, err
	}
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.RuntimeProfile{}).
		Watches(&agentv1alpha1.RuntimeHosting{}, handler.EnqueueRequestsFromMapFunc(r.profilesForHosting)).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile resolves the endpoint and publishes it in status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	profile := &agentv1alpha1.RuntimeProfile{}
	if err := r.Get(ctx, req.NamespacedName, profile); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	before := profile.DeepCopy()
	profile.Status.ObservedGeneration = profile.Generation

	endpoint, ready, reason, message := r.resolve(ctx, profile)
	profile.Status.ResolvedEndpoint = endpoint
	profile.Status.Capabilities = capabilitiesOf(profile)
	support.SetReady(r.Recorder, profile, &profile.Status.Conditions, profile.Generation, ready, reason, message)

	if apiequality.Semantic.DeepEqual(&before.Status, &profile.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, profile, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) resolve(
	ctx context.Context, profile *agentv1alpha1.RuntimeProfile,
) (endpoint string, ready bool, reason, message string) {
	switch profile.Spec.Kind {
	case "", agentv1alpha1.RuntimeKindInproc:
		return "", true, "InProcess", "the runtime executes in this process"
	case agentv1alpha1.RuntimeKindA2A:
	default:
		return "", false, "UnsupportedKind", fmt.Sprintf("unsupported runtime kind %q", profile.Spec.Kind)
	}

	if profile.Spec.ClientConfig != nil {
		resolved, err := clientconfig.Resolve(*profile.Spec.ClientConfig)
		if err != nil {
			return "", false, "InvalidClientConfig", err.Error()
		}
		return resolved.URL, true, "ClientConfigDeclared", "using the declared clientConfig endpoint"
	}
	if profile.Spec.Endpoint != "" {
		if err := validateEndpoint(profile.Spec.Endpoint); err != nil {
			return "", false, "InvalidEndpoint", err.Error()
		}
		return profile.Spec.Endpoint, true, "EndpointDeclared", "using the declared endpoint"
	}
	if profile.Spec.HostingRef == nil {
		return "", false, "EndpointMissing", "an a2a profile needs an endpoint or a hostingRef"
	}

	hosting := &agentv1alpha1.RuntimeHosting{}
	key := types.NamespacedName{Namespace: profile.Namespace, Name: profile.Spec.HostingRef.Name}
	if err := r.Get(ctx, key, hosting); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return "", false, "HostingNotFound", fmt.Sprintf("RuntimeHosting %q does not exist", key.Name)
		}
		return "", false, "HostingLookupFailed", err.Error()
	}
	for _, instance := range hosting.Status.Instances {
		if instance.State != "ready" || instance.Endpoint == "" {
			continue
		}
		if err := validateEndpoint(instance.Endpoint); err != nil {
			return "", false, "InvalidHostingEndpoint", err.Error()
		}
		return instance.Endpoint, true, "Hosted", fmt.Sprintf("instance %s is ready", instance.ID)
	}
	return "", false, "HostingPending", fmt.Sprintf("hosting %q has no ready instance", key.Name)
}

func validateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("endpoint %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("endpoint %q must be http or https", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("endpoint %q has no host", raw)
	}
	return nil
}

func capabilitiesOf(profile *agentv1alpha1.RuntimeProfile) agentv1alpha1.RuntimeCapabilities {
	caps := agentv1alpha1.RuntimeCapabilities{
		Streaming:   true,
		HistoryMode: agentv1alpha1.HistoryModeGateway,
		ResumeMode:  agentv1alpha1.ResumeModeCheckpoint,
	}
	if profile.Spec.Kind == agentv1alpha1.RuntimeKindA2A {
		caps.HistoryMode = agentv1alpha1.HistoryModeRuntime
		caps.ResumeMode = agentv1alpha1.ResumeModeTask
	}
	return caps
}

func (r *Reconciler) profilesForHosting(ctx context.Context, obj client.Object) []reconcile.Request {
	profiles := &agentv1alpha1.RuntimeProfileList{}
	if err := r.List(ctx, profiles, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range profiles.Items {
		profile := profiles.Items[i]
		if profile.Spec.HostingRef == nil || profile.Spec.HostingRef.Name != obj.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: profile.Namespace, Name: profile.Name,
		}})
	}
	return requests
}
