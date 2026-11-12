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

// Package extensions exposes the control plane's own extension objects — the
// admission webhook configurations, the CEL admission policies bound to them
// and the aggregated API registrations — to the console.
//
// It deliberately does not model these kinds as Go structs. Every one of them
// is stored and served by oai-declserver as JSON, and the console's job is to
// let an administrator read and edit that JSON: a second Go type per kind would
// have to be kept in step with four separate upstream API groups, and the
// console would still have no field it did not know about. Passing unstructured
// objects through keeps the served shape the only contract.
package extensions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Errors the biz layer raises itself. The handler maps them onto HTTP status
// codes, so a caller can tell "no such kind" from "you forgot the version".
var (
	// ErrUnknownKind reports a URL segment that is not one of Kinds.
	ErrUnknownKind = errors.New("unknown extension kind")
	// ErrNameRequired reports a body with no metadata.name.
	ErrNameRequired = errors.New("metadata.name is required")
	// ErrResourceVersionRequired reports an update that did not load the object
	// first, which would be a blind overwrite.
	ErrResourceVersionRequired = errors.New("metadata.resourceVersion is required: load the object before editing it")
)

// Kind is one extension object this surface manages.
type Kind struct {
	// Resource is both the URL segment and the API resource name, which is why
	// the console can address an object with the same word the API server uses.
	Resource string
	// ObjectKind is the API kind, used to stamp objects the console creates.
	ObjectKind string
	// Group and Version identify the API group version that serves it.
	Group   string
	Version string
	// DisplayName is what the console lists it as.
	DisplayName string
	// Description explains what the kind is for, in the product's terms.
	Description string
}

// GroupVersionResource returns the GVR used to reach the kind.
func (k Kind) GroupVersionResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
}

// Kinds are the objects this surface manages, in menu order.
//
// All of them are cluster-scoped: an admission webhook configuration, a CEL
// policy, its binding and an APIService belong to the cluster, not to a
// namespace, so no request here carries one.
var Kinds = []Kind{
	{
		Resource:    "validatingwebhookconfigurations",
		ObjectKind:  "ValidatingWebhookConfiguration",
		Group:       "admissionregistration.k8s.io",
		Version:     "v1",
		DisplayName: "校验式 Webhook",
		Description: "准入链上的外部校验回调；拒绝即阻止写入",
	},
	{
		Resource:    "mutatingwebhookconfigurations",
		ObjectKind:  "MutatingWebhookConfiguration",
		Group:       "admissionregistration.k8s.io",
		Version:     "v1",
		DisplayName: "变更式 Webhook",
		Description: "准入链上的外部变更回调；可改写对象后再校验",
	},
	{
		Resource:    "validatingadmissionpolicies",
		ObjectKind:  "ValidatingAdmissionPolicy",
		Group:       "admissionregistration.k8s.io",
		Version:     "v1",
		DisplayName: "CEL 校验策略",
		Description: "进程内 CEL 校验，无需外部服务",
	},
	{
		Resource:    "validatingadmissionpolicybindings",
		ObjectKind:  "ValidatingAdmissionPolicyBinding",
		Group:       "admissionregistration.k8s.io",
		Version:     "v1",
		DisplayName: "CEL 校验策略绑定",
		Description: "把 CEL 校验策略绑定到匹配的资源上",
	},
	{
		Resource:    "mutatingadmissionpolicies",
		ObjectKind:  "MutatingAdmissionPolicy",
		Group:       "admissionregistration.k8s.io",
		Version:     "v1",
		DisplayName: "CEL 变更策略",
		Description: "进程内 CEL 变更（ApplyConfiguration）",
	},
	{
		Resource:    "mutatingadmissionpolicybindings",
		ObjectKind:  "MutatingAdmissionPolicyBinding",
		Group:       "admissionregistration.k8s.io",
		Version:     "v1",
		DisplayName: "CEL 变更策略绑定",
		Description: "把 CEL 变更策略绑定到匹配的资源上",
	},
	{
		Resource:    "apiservices",
		ObjectKind:  "APIService",
		Group:       "apiregistration.k8s.io",
		Version:     "v1",
		DisplayName: "聚合 API 注册",
		Description: "把外部 API Server 的组版本挂到本控制面下",
	},
}

// KindByResource resolves a URL segment to its kind.
func KindByResource(resource string) (Kind, bool) {
	for _, k := range Kinds {
		if k.Resource == resource {
			return k, true
		}
	}
	return Kind{}, false
}

// Biz reads and writes the extension objects.
type Biz interface {
	// Kinds describes what the surface manages, for the console's menu.
	Kinds() []Kind
	// List returns every object of the kind.
	List(ctx context.Context, resource string) (*unstructured.UnstructuredList, error)
	// Get returns one object.
	Get(ctx context.Context, resource, name string) (*unstructured.Unstructured, error)
	// Create stores a new object.
	Create(ctx context.Context, resource string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	// Update replaces an existing object. The submitted resourceVersion decides
	// whether the write wins, so a console tab that loaded a stale copy gets a
	// conflict instead of silently reverting somebody else's edit.
	Update(ctx context.Context, resource string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	// Delete removes an object.
	Delete(ctx context.Context, resource, name string) error
}

type biz struct {
	client dynamic.Interface
}

// New returns a Biz over the control plane's dynamic client.
func New(client dynamic.Interface) Biz {
	if client == nil {
		return nil
	}
	return &biz{client: client}
}

// Kinds implements Biz.
func (b *biz) Kinds() []Kind { return Kinds }

// List implements Biz. The result is sorted by name so the console's list is
// stable: the API server does not promise an order for a cluster-scoped list.
func (b *biz) List(ctx context.Context, resource string) (*unstructured.UnstructuredList, error) {
	kind, ok := KindByResource(resource)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, resource)
	}
	list, err := b.client.Resource(kind.GroupVersionResource()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].GetName() < list.Items[j].GetName() })
	return list, nil
}

// Get implements Biz.
func (b *biz) Get(ctx context.Context, resource, name string) (*unstructured.Unstructured, error) {
	kind, ok := KindByResource(resource)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, resource)
	}
	if strings.TrimSpace(name) == "" {
		return nil, ErrNameRequired
	}
	return b.client.Resource(kind.GroupVersionResource()).Get(ctx, name, metav1.GetOptions{})
}

// Create implements Biz.
func (b *biz) Create(ctx context.Context, resource string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	kind, ok := KindByResource(resource)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, resource)
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: kind.Group, Version: kind.Version, Kind: kind.ObjectKind})
	if obj.GetName() == "" {
		return nil, ErrNameRequired
	}
	// The console creates; it never adopts. A name that already exists means the
	// administrator is looking at a stale list, and a conflict says so.
	return b.client.Resource(kind.GroupVersionResource()).Create(ctx, obj, metav1.CreateOptions{})
}

// Update implements Biz.
func (b *biz) Update(ctx context.Context, resource string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	kind, ok := KindByResource(resource)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, resource)
	}
	name := obj.GetName()
	if strings.TrimSpace(name) == "" {
		return nil, ErrNameRequired
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: kind.Group, Version: kind.Version, Kind: kind.ObjectKind})
	if obj.GetResourceVersion() == "" {
		// Read-modify-write without a version is a blind overwrite; the console
		// always loads the object first, so a missing version means the caller
		// built the payload itself. Refuse rather than guess.
		return nil, ErrResourceVersionRequired
	}
	return b.client.Resource(kind.GroupVersionResource()).Update(ctx, obj, metav1.UpdateOptions{})
}

// Delete implements Biz.
func (b *biz) Delete(ctx context.Context, resource, name string) error {
	kind, ok := KindByResource(resource)
	if !ok {
		return fmt.Errorf("unknown extension kind %q", resource)
	}
	if strings.TrimSpace(name) == "" {
		return ErrNameRequired
	}
	return b.client.Resource(kind.GroupVersionResource()).Delete(ctx, name, metav1.DeleteOptions{})
}

// IsNotFound reports whether err is the API server's 404.
func IsNotFound(err error) bool { return apierrors.IsNotFound(err) }
