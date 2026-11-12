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

// Package declclient reads the declarative control plane (oai-declserver).
//
// It is the single place a runtime service builds the typed clientset and the
// shared informer factory, so agent and chat agree on the kubeconfig, the
// namespace scope and the cache-sync rules instead of each inventing its own.
package declclient

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	aggregatorclientset "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset"

	kubeinformers "k8s.io/client-go/informers"

	versioned "github.com/onexstack/onexai/pkg/generated/clientset/versioned"
	informers "github.com/onexstack/onexai/pkg/generated/informers/externalversions"
	"github.com/onexstack/onexai/pkg/readmodel"
)

// Client owns the clientset and the shared informer factory.
type Client struct {
	// Clientset is the typed client for this project's groups.
	Clientset versioned.Interface
	// KubeClient reads core objects (Secrets) served by the same API server.
	// Its AdmissionregistrationV1() client reaches the webhook and CEL policy
	// objects the extension console manages.
	KubeClient kubernetes.Interface
	// AggregatorClient reads apiregistration.k8s.io APIService, which lives in
	// the kube-aggregator clientset rather than in kubernetes.Interface.
	AggregatorClient aggregatorclientset.Interface
	// DynamicClient reads and writes arbitrary objects the control plane serves,
	// which is what the extension console needs: it manages kinds that no single
	// typed clientset covers, and it passes them through as the JSON the API
	// server stores rather than mapping them onto a Go type per kind.
	DynamicClient dynamic.Interface
	// Factory is the shared informer factory for this project's groups.
	// Consumers add their own event handlers to it before Start.
	Factory informers.SharedInformerFactory
	// KubeFactory watches the standard k8s objects the same API server serves
	// (RBAC bindings, ConfigMaps), so role membership and runtime settings are
	// read from the control plane rather than from a database of their own.
	KubeFactory kubeinformers.SharedInformerFactory

	namespace string

	// informers is the kind -> informer table, built once at construction.
	// Registration and the lister accessors read the same table, so a kind
	// cannot be registered for one and forgotten by the other.
	informers   map[string]func() cache.SharedIndexInformer
	informersMu sync.Mutex

	// skipped holds the kinds whose informer this process deliberately does not
	// run. A filtered kind's lister is backed by an empty indexer, so touching it
	// cannot create the informer it was meant to avoid — client-go starts an
	// informer added after Start, which would have made the saving imaginary.
	skipped map[string]struct{}
}

// Option customises New.
type Option func(*Client)

// WithInformerFilter excludes kinds from the informer set. It must be applied at
// construction: an informer requested later is started immediately by client-go,
// so filtering afterwards saves nothing.
func WithInformerFilter(filter func(schema.GroupVersionKind) bool) Option {
	return func(c *Client) {
		if filter == nil {
			return
		}
		for _, ref := range readmodel.Kinds {
			gvk := schema.GroupVersionKind{Group: ref.Group, Version: "v1alpha1", Kind: ref.Kind}
			if filter(gvk) {
				continue
			}
			if c.skipped == nil {
				c.skipped = make(map[string]struct{})
			}
			c.skipped[ref.Group+"/"+ref.Kind] = struct{}{}
		}
	}
}

// New builds a client from a kubeconfig path. namespace narrows every watch;
// empty watches all namespaces. resync is the informer's periodic full resync,
// a safety net behind the watch.
func New(kubeconfig, namespace string, resync time.Duration, options ...Option) (*Client, error) {
	if kubeconfig == "" {
		return nil, fmt.Errorf("declclient: no kubeconfig configured")
	}

	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("declclient: load kubeconfig %q: %w", kubeconfig, err)
	}
	// The control plane is a small API server, not a kube-apiserver: a modest
	// client-side limit keeps a burst of resyncs from looking like an attack.
	restConfig.QPS = 50
	restConfig.Burst = 100
	restConfig.UserAgent = "onexai-runtime"

	clientset, err := versioned.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("declclient: build clientset: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("declclient: build kubernetes client: %w", err)
	}
	aggregatorClient, err := aggregatorclientset.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("declclient: build aggregator client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("declclient: build dynamic client: %w", err)
	}

	var opts []informers.SharedInformerOption
	if namespace != "" {
		opts = append(opts, informers.WithNamespace(namespace))
	}
	factory := informers.NewSharedInformerFactoryWithOptions(clientset, resync, opts...)

	// Standard objects are cluster-scoped (bindings) or namespace-scoped
	// (ConfigMaps); a single namespace scope is applied through the listers
	// instead of the factory, so the cluster-scoped bindings stay visible.
	kubeFactory := kubeinformers.NewSharedInformerFactory(kubeClient, resync)

	client := &Client{
		Clientset:        clientset,
		KubeClient:       kubeClient,
		AggregatorClient: aggregatorClient,
		DynamicClient:    dynamicClient,
		Factory:          factory,
		KubeFactory:      kubeFactory,
		namespace:        namespace,
	}
	for _, opt := range options {
		opt(client)
	}
	client.informers = client.buildInformers()
	client.registerInformers()
	return client, nil
}

// Namespace returns the scope this client was built for.
func (c *Client) Namespace() string { return c.namespace }

// Start runs every registered informer and waits for the first cache sync.
//
// A sync that does not finish within timeout is a start-up failure, not a
// warning: serving requests against an empty cache would deny every request for
// reasons the operator cannot see.
func (c *Client) Start(ctx context.Context, timeout time.Duration) error {
	c.Factory.Start(ctx.Done())
	c.KubeFactory.Start(ctx.Done())

	syncCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan map[reflect.Type]bool, 1)
	go func() {
		synced := c.Factory.WaitForCacheSync(syncCtx.Done())
		for typ, ok := range c.KubeFactory.WaitForCacheSync(syncCtx.Done()) {
			synced[typ] = ok
		}
		done <- synced
	}()

	select {
	case <-syncCtx.Done():
		return fmt.Errorf("declclient: informer caches did not sync within %s: %w", timeout, syncCtx.Err())
	case result := <-done:
		for typ, ok := range result {
			if !ok {
				return fmt.Errorf("declclient: informer for %s failed to sync", typ)
			}
		}
		return nil
	}
}
