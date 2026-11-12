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

package job_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// memClient is a deliberately small in-memory client.Client for the batch
// controllers. controller-runtime's fake package is unusable here: it is built
// against structured-merge-diff v6 while this project's k8s libraries use v7,
// so the two TypeConverter interfaces do not match.
//
// It implements exactly what these controllers call: Get, List, Create, Update,
// Delete and Status().Update. The rest of the interface refuses with
// "method not supported" rather than pretending to work.
type memClient struct {
	scheme  *runtime.Scheme
	objects map[schema.GroupVersionKind]map[types.NamespacedName]runtime.Object
}

var _ client.Client = (*memClient)(nil)

func newMemClient(scheme *runtime.Scheme, initial ...client.Object) *memClient {
	c := &memClient{
		scheme:  scheme,
		objects: map[schema.GroupVersionKind]map[types.NamespacedName]runtime.Object{},
	}
	for _, obj := range initial {
		if err := c.Create(context.Background(), obj); err != nil {
			panic(err)
		}
	}
	return c
}

func (c *memClient) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	gvk, err := c.gvkFor(obj)
	if err != nil {
		return err
	}
	stored, ok := c.objects[gvk][types.NamespacedName(key)]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: gvk.Kind}, key.Name)
	}
	replaceObject(obj, stored)
	return nil
}

func (c *memClient) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	var options client.ListOptions
	for _, opt := range opts {
		opt.ApplyToList(&options)
	}

	gvk, err := c.gvkFor(list)
	if err != nil {
		return err
	}
	itemGVK := gvk
	itemGVK.Kind = strings.TrimSuffix(gvk.Kind, "List")
	if itemGVK.Kind == gvk.Kind {
		return fmt.Errorf("memClient: %s is not a list type", gvk.Kind)
	}

	items := make([]runtime.Object, 0, len(c.objects[itemGVK]))
	for key, obj := range c.objects[itemGVK] {
		if options.Namespace != "" && key.Namespace != options.Namespace {
			continue
		}
		items = append(items, obj.DeepCopyObject())
	}
	return meta.SetList(list, items)
}

func (c *memClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	gvk, err := c.gvkFor(obj)
	if err != nil {
		return err
	}
	key := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}
	if gvkGroup := c.objects[gvk]; gvkGroup != nil {
		if _, exists := gvkGroup[key]; exists {
			return apierrors.NewAlreadyExists(schema.GroupResource{Group: gvk.Group, Resource: gvk.Kind}, key.Name)
		}
	} else {
		c.objects[gvk] = map[types.NamespacedName]runtime.Object{}
	}

	copied := obj.DeepCopyObject().(client.Object)
	if copied.GetUID() == "" {
		copied.SetUID(types.UID(uuid.NewString()))
	}
	if copied.GetResourceVersion() == "" {
		copied.SetResourceVersion("1")
	}
	c.objects[gvk][key] = copied
	replaceObject(obj, copied)
	return nil
}

func (c *memClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	gvk, err := c.gvkFor(obj)
	if err != nil {
		return err
	}
	key := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}
	if _, ok := c.objects[gvk][key]; !ok {
		return apierrors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: gvk.Kind}, key.Name)
	}
	next := resourceVersion(obj.GetResourceVersion())
	obj.SetResourceVersion(next)
	copied := obj.DeepCopyObject().(client.Object)
	c.objects[gvk][key] = copied
	replaceObject(obj, copied)
	return nil
}

func (c *memClient) Delete(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
	gvk, err := c.gvkFor(obj)
	if err != nil {
		return err
	}
	key := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}
	if _, ok := c.objects[gvk][key]; !ok {
		return apierrors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: gvk.Kind}, key.Name)
	}
	delete(c.objects[gvk], key)
	return nil
}

func (c *memClient) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return fmt.Errorf("memClient: Patch is not supported")
}

func (c *memClient) DeleteAllOf(context.Context, client.Object, ...client.DeleteAllOfOption) error {
	return fmt.Errorf("memClient: DeleteAllOf is not supported")
}

func (c *memClient) Apply(context.Context, runtime.ApplyConfiguration, ...client.ApplyOption) error {
	return fmt.Errorf("memClient: Apply is not supported")
}

func (c *memClient) Status() client.SubResourceWriter { return memSubResource{c: c} }

func (c *memClient) SubResource(string) client.SubResourceClient { return memSubResource{c: c} }

func (c *memClient) Scheme() *runtime.Scheme { return c.scheme }

func (c *memClient) RESTMapper() meta.RESTMapper { return nil }

func (c *memClient) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return c.gvkFor(obj)
}

func (c *memClient) IsObjectNamespaced(runtime.Object) (bool, error) { return true, nil }

func (c *memClient) gvkFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	kinds, _, err := c.scheme.ObjectKinds(obj)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	if len(kinds) == 0 {
		return schema.GroupVersionKind{}, fmt.Errorf("memClient: no kind registered for %T", obj)
	}
	return kinds[0], nil
}

// memSubResource implements both SubResourceClient and SubResourceWriter. The
// controllers only use Status().Update, which writes the whole object.
type memSubResource struct{ c *memClient }

var (
	_ client.SubResourceClient = memSubResource{}
	_ client.SubResourceWriter = memSubResource{}
)

func (s memSubResource) Get(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceGetOption) error {
	return s.c.Get(ctx, client.ObjectKeyFromObject(obj), subResource)
}

func (s memSubResource) Create(ctx context.Context, obj client.Object, subResource client.Object, _ ...client.SubResourceCreateOption) error {
	return s.c.Create(ctx, subResource)
}

func (s memSubResource) Update(ctx context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
	return s.c.Update(ctx, obj)
}

func (s memSubResource) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return fmt.Errorf("memClient: subresource Patch is not supported")
}

func (s memSubResource) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return fmt.Errorf("memClient: subresource Apply is not supported")
}

// replaceObject copies src into the caller's object. Both sides are the same
// concrete type in every call the tests make, so a reflective assignment of the
// pointed-to value is safe and avoids a per-kind switch.
func replaceObject(dst, src runtime.Object) {
	dv := reflect.ValueOf(dst)
	sv := reflect.ValueOf(src)
	if dv.Kind() != reflect.Ptr || dv.IsNil() {
		return
	}
	if sv.Kind() == reflect.Ptr {
		if sv.IsNil() {
			return
		}
		sv = sv.Elem()
	}
	dv.Elem().Set(sv)
}

func resourceVersion(current string) string {
	if current == "" {
		return "1"
	}
	var n int
	if _, err := fmt.Sscanf(current, "%d", &n); err != nil {
		return "2"
	}
	return fmt.Sprintf("%d", n+1)
}
