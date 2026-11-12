// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

// Package app does all of the work necessary to create a OneX
// APIServer by binding together the API, master and APIServer infrastructure.
//
//nolint:nakedret
package apiserver

import (
	"fmt"
	"net/url"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/admission"
	webhookinitializer "k8s.io/apiserver/pkg/admission/plugin/webhook/initializer"
	utilflowcontrol "k8s.io/apiserver/pkg/util/flowcontrol"
	"k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	aggregatorapiserver "k8s.io/kube-aggregator/pkg/apiserver"

	//"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/discovery/aggregated"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/apiserver/pkg/reconcilers"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/filters"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
	"k8s.io/apiserver/pkg/util/openapi"
	utilpeerproxy "k8s.io/apiserver/pkg/util/peerproxy"
	"k8s.io/client-go/informers"
	coordinationv1informers "k8s.io/client-go/informers/coordination/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/transport"
	openapicommon "k8s.io/kube-openapi/pkg/common"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"k8s.io/kubernetes/pkg/kubeapiserver"

	onexadmissioninitializer "github.com/onexstack/onexai/internal/apiserver/admission/initializer"
	"github.com/onexstack/onexai/internal/controlplane"
	controlplaneoptions "github.com/onexstack/onexai/internal/controlplane/apiserver/options"
	onexclientset "github.com/onexstack/onexai/pkg/generated/clientset/versioned"
)

const (
	// DefaultPeerEndpointReconcileInterval is the default amount of time for how often
	// the peer endpoint leases are reconciled.
	DefaultPeerEndpointReconcileInterval = 10 * time.Second
	// DefaultPeerEndpointReconcilerTTL is the default TTL timeout for peer endpoint
	// leases on the storage layer
	DefaultPeerEndpointReconcilerTTL = 15 * time.Second

	// IdentityLeaseComponentLabelKey is used to apply a component label to identity lease objects, indicating:
	//   1. the lease is an identity lease (different from leader election leases)
	//   2. which component owns this lease
	IdentityLeaseComponentLabelKey = "apiserver.kubernetes.io/identity"

	// KubeAPIServer defines variable used internally when referring to kube-apiserver component
	KubeAPIServer = "kube-apiserver"
)

// BuildGenericConfig takes the master server options and produces the genericapiserver.Config associated with it.
func BuildGenericConfig(
	s controlplaneoptions.CompletedOptions,
	schemes []*runtime.Scheme,
	getOpenAPIDefinitions func(ref openapicommon.ReferenceCallback) map[string]openapicommon.OpenAPIDefinition,
) (
	genericConfig *genericapiserver.RecommendedConfig,
	kubeSharedInformers informers.SharedInformerFactory,
	storageFactory *serverstorage.DefaultStorageFactory,
	lastErr error,
) {
	genericConfig = genericapiserver.NewRecommendedConfig(legacyscheme.Codecs)
	genericConfig.MergedResourceConfig = controlplane.DefaultAPIResourceConfigSource()

	if lastErr = s.GenericServerRunOptions.ApplyTo(&genericConfig.Config); lastErr != nil {
		return
	}

	s.RecommendedOptions.ExtraAdmissionInitializers = func(c *genericapiserver.RecommendedConfig) ([]admission.PluginInitializer, error) {
		client, err := clientset.NewForConfig(c.LoopbackClientConfig)
		if err != nil {
			return nil, err
		}

		// onex's own API groups are not part of the Kubernetes clientset, so a
		// plugin that reads Agents needs the generated clientset. The content
		// type is still JSON here; LoopbackClientConfig is switched to protobuf
		// further down, after the initializers have run.
		onexClient, err := onexclientset.NewForConfig(c.LoopbackClientConfig)
		if err != nil {
			return nil, fmt.Errorf("create onex clientset for admission: %w", err)
		}

		// Webhook admission plugins resolve `service:` references through a
		// service resolver and authenticate outbound calls via an
		// authentication-info resolver wrapper. Both are independent of the
		// onex custom initializer, so wire dedicated initializers for them.
		//
		// The resolver reads the real core/v1 Services this server now serves, so
		// a `service:` reference in a webhook configuration is actually resolved
		// instead of failing with "no service".
		serviceResolver := aggregatorapiserver.NewClusterIPServiceResolver(
			c.SharedInformerFactory.Core().V1().Services().Lister(),
		)
		if localHost, err := url.Parse(c.LoopbackClientConfig.Host); err == nil {
			serviceResolver = aggregatorapiserver.NewLoopbackServiceResolver(serviceResolver, localHost)
		}

		authInfoResolverWrapper := webhook.NewDefaultAuthenticationInfoResolverWrapper(
			nil, // proxyTransport: not wired yet, outgoing webhook calls use the default transport
			c.EgressSelector,
			c.LoopbackClientConfig,
			c.TracerProvider,
		)
		webhookInitializer := webhookinitializer.NewPluginInitializer(authInfoResolverWrapper, serviceResolver)

		// No explicit admissioninitializer.New here: AdmissionOptions.ApplyTo
		// already prepends the generic initializer carrying the real authorizer,
		// dynamic client, REST mapper and drained notification. Registering a
		// second one with nils would run after it and wipe those fields, which is
		// exactly what CEL admission policies need.
		return []admission.PluginInitializer{
			webhookInitializer,
			onexadmissioninitializer.NewPluginInitializer(onexClient, client),
		}, nil
	}

	// RecommendedOptions.ApplyTo must after RecommendedOptions.ExtraAdmissionInitializers.
	// Because RecommendedOptions.ApplyTo need init ExtraAdmissionInitializers.
	if lastErr = s.RecommendedOptions.ApplyTo(genericConfig); lastErr != nil {
		return
	}

	// Use protobufs for self-communication.
	// Since not every generic apiserver has to support protobufs, we
	// cannot default to it in generic apiserver and need to explicitly
	// set it in oai-declserver.
	genericConfig.LoopbackClientConfig.ContentConfig.ContentType = "application/vnd.kubernetes.protobuf"
	// Disable compression for self-communication, since we are going to be
	// on a fast local network
	genericConfig.LoopbackClientConfig.DisableCompression = true

	loopbackClientConfig := genericConfig.LoopbackClientConfig

	// Build kubernetes client
	// Use onex's config to mock a kubernetes client.
	kubeClient, err := clientset.NewForConfig(loopbackClientConfig)
	if err != nil {
		lastErr = fmt.Errorf("failed to create real external clientset: %v", err)
		return
	}
	// The dynamic client is what CEL admission policies use to read the objects
	// their bindings name as params. It is created here so authentication,
	// authorization and admission all share one view of the loopback API.
	dynamicClient, err := dynamic.NewForConfig(loopbackClientConfig)
	if err != nil {
		lastErr = fmt.Errorf("failed to create dynamic client: %v", err)
		return
	}
	kubeSharedInformers = informers.NewSharedInformerFactory(kubeClient, loopbackClientConfig.Timeout)
	// Hand the factory to the generic server so that CompletedConfig.New installs
	// the "generic-apiserver-start-informers" post-start hook and the
	// informer-sync readyz check, exactly like kube-apiserver does. Without this
	// the factory is created and consumed but never started, which leaves the
	// aggregator service resolver and the endpoint-slice getter permanently empty.
	genericConfig.SharedInformerFactory = kubeSharedInformers
	// One factory, one view of the control plane: API priority and fairness, the
	// flow-control storages and the admission chain all read from it.
	s.InternalVersionedInformers = kubeSharedInformers

	if lastErr = s.Features.ApplyTo(&genericConfig.Config, kubeClient, kubeSharedInformers); lastErr != nil {
		return
	}
	if lastErr = s.APIEnablement.ApplyTo(&genericConfig.Config, controlplane.DefaultAPIResourceConfigSource(), legacyscheme.Scheme); lastErr != nil {
		return
	}

	// Traces and Audit are applied by RecommendedOptions.ApplyTo above; applying
	// them a second time here would double-register their metrics.
	// Wire API Priority and Fairness (APF) into the request-handling filter
	// chain. The flow-control REST storage (FlowSchema/PriorityLevelConfiguration
	// CRUD) and its bootstrap ensurer are already installed via the REST storage
	// provider; setting FlowControl here is what actually routes requests through
	// the APF queue-set dispatch. Until the flow-control informers sync, APF falls
	// back to its built-in catch-all configuration, so requests are not dropped
	// during startup.
	genericConfig.FlowControl = utilflowcontrol.New(
		s.InternalVersionedInformers,
		kubeClient.FlowcontrolV1(),
		genericConfig.MaxRequestsInFlight+genericConfig.MaxMutatingRequestsInFlight,
	)

	// wrap the definitions to revert any changes from disabled features
	getOpenAPIDefinitions = openapi.GetOpenAPIDefinitionsWithoutDisabledFeatures(getOpenAPIDefinitions)
	// namer := openapinamer.NewDefinitionNamer(legacyscheme.Scheme)
	namer := openapinamer.NewDefinitionNamer(schemes...)
	genericConfig.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(getOpenAPIDefinitions, namer)
	genericConfig.OpenAPIConfig.Info.Title = "OneX"
	genericConfig.OpenAPIConfig.Info.Version = "v0.0.1"
	genericConfig.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(getOpenAPIDefinitions, namer)
	genericConfig.OpenAPIV3Config.Info.Title = "OneX"
	genericConfig.OpenAPIV3Config.Info.Version = "v0.0.1"
	// Not in use, just serving as a placeholder.
	genericConfig.LongRunningFunc = filters.BasicLongRunningRequestCheck(
		sets.NewString("watch", "proxy"),
		sets.NewString("attach", "exec", "proxy", "log", "portforward"),
	)

	if genericConfig.EgressSelector != nil {
		s.RecommendedOptions.Etcd.StorageConfig.Transport.EgressLookup = genericConfig.EgressSelector.Lookup
	}
	s.RecommendedOptions.Etcd.StorageConfig.Transport.TracerProvider = genericConfig.TracerProvider

	// Use the upstream factory configuration. The local fork existed only to
	// override SpecialDefaultResourcePrefixes and the events watch-cache size,
	// both of which upstream already sets the same way.
	storageFactoryConfig := kubeapiserver.NewStorageFactoryConfig()
	storageFactoryConfig.APIResourceConfig = genericConfig.MergedResourceConfig
	storageFactoryConfig.DefaultResourceEncoding.SetEffectiveVersion(genericConfig.EffectiveVersion)
	storageFactory, lastErr = storageFactoryConfig.Complete(s.RecommendedOptions.Etcd).New()
	if lastErr != nil {
		return
	}
	if lastErr = s.RecommendedOptions.Etcd.ApplyWithStorageFactoryTo(storageFactory, &genericConfig.Config); lastErr != nil {
		return
	}

	// Both halves of access control are the upstream implementations, so every
	// documented kube-apiserver extension point behaves identically here:
	// --authentication-config / --authorization-config (declarative files),
	// --authentication-token-webhook-config-file (TokenReview), --client-ca-file,
	// --requestheader-*, --token-auth-file, --oidc-*, --authorization-mode and
	// --authorization-webhook-config-file.
	//
	// The context is the server's drained channel: the OIDC key refresher and the
	// authorization-config reloader stop with the process instead of leaking.
	ctx := wait.ContextForChannel(genericConfig.DrainedNotify())

	if lastErr = s.Authentication.ApplyTo(
		ctx,
		&genericConfig.Authentication,
		genericConfig.SecureServing,
		genericConfig.EgressSelector,
		genericConfig.OpenAPIConfig,
		genericConfig.OpenAPIV3Config,
		kubeClient,
		// A throwaway factory, deliberately never started. ApplyTo unconditionally
		// builds the service-account token getter, which registers Secrets,
		// ServiceAccounts, Pods and Nodes informers; this server serves none of
		// them, so putting them on the shared factory would leave its sync
		// readiness check failing forever. The getter is only ever called when a
		// service-account authenticator exists, and that needs
		// ServiceAccounts options this server never enables.
		informers.NewSharedInformerFactory(kubeClient, 0),
		genericConfig.APIServerID,
	); lastErr != nil {
		return
	}

	authorizerConfig, err := s.Authorization.ToAuthorizationConfig(kubeSharedInformers)
	if err != nil {
		lastErr = fmt.Errorf("invalid authorization config: %w", err)
		return
	}
	genericConfig.Authorization.Authorizer, genericConfig.RuleResolver, err = authorizerConfig.New(ctx, genericConfig.APIServerID)
	if err != nil {
		lastErr = fmt.Errorf("failed to build authorizer: %w", err)
		return
	}

	// Admission runs last so its generic initializer captures the authorizer
	// above; CEL admission policies need it to read their param objects.
	if lastErr = s.RecommendedOptions.ApplyAdmission(genericConfig, kubeClient, dynamicClient); lastErr != nil {
		return
	}

	genericConfig.AggregatedDiscoveryGroupManager = aggregated.NewResourceManager("apis")
	return
}

func BuildPeerProxy(
	leaseInformer coordinationv1informers.LeaseInformer,
	loopbackClientConfig *rest.Config,
	proxyClientCertFile string,
	proxyClientKeyFile string,
	peerCAFile string,
	peerAdvertiseAddress reconcilers.PeerAdvertiseAddress,
	apiServerID string,
	reconciler reconcilers.PeerEndpointLeaseReconciler,
	serializer runtime.NegotiatedSerializer,
) (utilpeerproxy.Interface, error) {
	if proxyClientCertFile == "" {
		return nil, fmt.Errorf("error building peer proxy handler, proxy-cert-file not specified")
	}
	if proxyClientKeyFile == "" {
		return nil, fmt.Errorf("error building peer proxy handler, proxy-key-file not specified")
	}

	proxyClientConfig := &transport.Config{
		TLS: transport.TLSConfig{
			Insecure:   false,
			CertFile:   proxyClientCertFile,
			KeyFile:    proxyClientKeyFile,
			CAFile:     peerCAFile,
			ServerName: "kubernetes.default.svc",
		},
	}

	return utilpeerproxy.NewPeerProxyHandler(
		apiServerID,
		IdentityLeaseComponentLabelKey+"="+KubeAPIServer,
		leaseInformer,
		reconciler,
		serializer,
		loopbackClientConfig,
		proxyClientConfig,
	)
}
