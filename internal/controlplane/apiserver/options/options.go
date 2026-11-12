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

// Package options contains flags and options for initializing an apiserver
package options

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	//"k8s.io/apimachinery/pkg/runtime"
	//"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiserver/pkg/admission"
	peerreconcilers "k8s.io/apiserver/pkg/reconcilers"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/informers"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"
	"k8s.io/component-base/metrics"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	kubeapiserveroptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
	netutils "k8s.io/utils/net"

	"github.com/onexstack/onexai/internal/pkg/options"
)

const defaultEtcdPathPrefix = "/registry/onexai.io"

// Options contains state for master/api server.
type Options struct {
	// RecommendedOptions *genericoptions.RecommendedOptions
	GenericServerRunOptions *genericoptions.ServerRunOptions
	RecommendedOptions      *options.RecommendedOptions
	Features                *genericoptions.FeatureOptions
	Metrics                 *metrics.Options
	Logs                    *logs.Options
	Traces                  *genericoptions.TracingOptions
	// CloudOptions            *cloud.CloudOptions
	APIEnablement *genericoptions.APIEnablementOptions
	AlternateDNS  []string

	ProxyClientCertFile string
	ProxyClientKeyFile  string

	// PeerCAFile is the ca bundle used by this kube-apiserver to verify peer apiservers'
	// serving certs when routing a request to the peer in the case the request can not be served
	// locally due to version skew.
	PeerCAFile string

	// PeerAdvertiseAddress is the IP for this kube-apiserver which is used by peer apiservers to route a request
	// to this apiserver. This happens in cases where the peer is not able to serve the request due to
	// version skew.
	PeerAdvertiseAddress peerreconcilers.PeerAdvertiseAddress

	EnableAggregatorRouting             bool
	AggregatorRejectForwardingRedirects bool

	// Authentication decides how a request to the secure port is authenticated:
	// static token files, client certificates, request-header (front proxy),
	// webhook token review, the legacy --oidc-* flags, and the declarative
	// apiserver.config.k8s.io/v1 AuthenticationConfiguration file.
	Authentication *kubeapiserveroptions.BuiltInAuthenticationOptions

	// Authorization decides how an authenticated request is authorized. It
	// accepts the kube-apiserver flags (--authorization-mode,
	// --authorization-webhook-config-file, ...) as well as the declarative
	// apiserver.config.k8s.io/v1 AuthorizationConfiguration file.
	Authorization *kubeapiserveroptions.BuiltInAuthorizationOptions

	EnableLogsHandler          bool
	EventTTL                   time.Duration
	InternalVersionedInformers informers.SharedInformerFactory
}

// completedServerRunOptions is a private wrapper that enforces a call of Complete() before Run can be invoked.
type completedOptions struct {
	Options
}

type CompletedOptions struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedOptions
}

// NewOptions returns a new Options.
func NewOptions() *Options {
	o := &Options{
		GenericServerRunOptions: genericoptions.NewServerRunOptions(),
		RecommendedOptions: options.NewRecommendedOptions(
			defaultEtcdPathPrefix,
			legacyscheme.Codecs.LegacyCodec(corev1.SchemeGroupVersion), // NOTICE: [Custom API] Set default with corev1.SchemeGroupVersion
		),
		Features:      genericoptions.NewFeatureOptions(),
		Metrics:       metrics.NewOptions(),
		Logs:          logs.NewOptions(),
		Traces:        genericoptions.NewTracingOptions(),
		APIEnablement: genericoptions.NewAPIEnablementOptions(),

		// The authenticator set mirrors what a control plane actually serves
		// here. Anonymous is on so that local clients without credentials keep
		// working; static tokens, client certs, request-header (front proxy)
		// identity, the token-review webhook and the declarative
		// AuthenticationConfiguration file are the extendable surface.
		//
		// Service accounts and bootstrap tokens are deliberately absent: this
		// server serves no ServiceAccount token issuer, so the flags would be
		// accepted and silently do nothing.
		Authentication: kubeapiserveroptions.NewBuiltInAuthenticationOptions().
			WithAnonymous().
			WithClientCert().
			WithOIDC().
			WithRequestHeader().
			WithTokenFile().
			WithWebHook(),

		// AlwaysAllow by default, matching the flags this server has always
		// accepted; RBAC, Webhook and a declarative AuthorizationConfiguration
		// file are opt-in.
		Authorization: kubeapiserveroptions.NewBuiltInAuthorizationOptions(),

		AlternateDNS:            []string{"onexai.io"},
		EnableLogsHandler:       true,
		EventTTL:                2 * time.Hour,
		EnableAggregatorRouting: false,
		// CloudOptions: cloud.NewCloudOptions(),
	}
	o.Authorization.Modes = []string{"AlwaysAllow"}
	// This server serves authentication.k8s.io/v1 and authorization.k8s.io/v1 only,
	// and every webhook it is pointed at (the Agent's TokenReview/SAR endpoints, an
	// external PDP) speaks v1. kube-apiserver's flag defaults are v1beta1, which
	// would turn each call into a 404.
	o.Authentication.WebHook.Version = "v1"
	o.Authorization.WebhookVersion = "v1"

	// NOTICE: Commented out temporarily, no side effects observed.
	/*
		o.RecommendedOptions.Etcd.StorageConfig.EncodeVersioner = runtime.NewMultiGroupVersioner(
			v1beta1.SchemeGroupVersion,
			schema.GroupKind{Group: v1beta1.GroupName},
		)
	*/

	// Redirect the certificates output directory to avoid creating the "apiserver.local.config" directory in the root directory
	// and keep the root directory clean.
	o.RecommendedOptions.SecureServing.ServerCert.CertDirectory = "_output/certificates"

	// the following three lines remove dependence with kube-apiserver
	o.RecommendedOptions.Authorization = nil

	// Use the default kubeconfig at $HOME/.onex/config, expanding the home directory
	// so it can be used as a real filesystem path by the kubeconfig loader.
	if home, err := os.UserHomeDir(); err == nil {
		o.RecommendedOptions.CoreAPI.CoreAPIKubeconfigPath = filepath.Join(home, ".onex", "config")
	}
	if cfg := os.Getenv("ONEXCONFIG"); cfg != "" {
		o.RecommendedOptions.CoreAPI.CoreAPIKubeconfigPath = cfg
	}
	// We only register the plugin of oai-declserver,
	// so we need to clear the plugin registered by apiserver by default.
	o.RecommendedOptions.Admission.Plugins = admission.NewPlugins()

	// register all custom dmission plugins
	RegisterAllAdmissionPlugins(o.RecommendedOptions.Admission.Plugins)
	o.RecommendedOptions.Admission.RecommendedPluginOrder = AllOrderedPlugins
	o.RecommendedOptions.Admission.DefaultOffPlugins = DefaultOffAdmissionPlugins()

	// Overwrite the default for storage data format.
	o.RecommendedOptions.Etcd.DefaultStorageMediaType = "application/vnd.kubernetes.protobuf"
	return o
}

func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	// Add the generic flags.
	o.GenericServerRunOptions.AddUniversalFlags(fss.FlagSet("generic"))
	o.RecommendedOptions.AddFlags(fss.FlagSet("recommended"))
	o.Features.AddFlags(fss.FlagSet("features"))
	o.APIEnablement.AddFlags(fss.FlagSet("API enablement"))
	o.Metrics.AddFlags(fss.FlagSet("metrics"))
	logsapi.AddFlags(o.Logs, fss.FlagSet("logs"))
	o.Traces.AddFlags(fss.FlagSet("traces"))
	// o.CloudOptions.AddFlags(fss.FlagSet("cloud"))

	// Note: the weird ""+ in below lines seems to be the only way to get gofmt to
	// arrange these text blocks sensibly. Grrr.
	fs := fss.FlagSet("misc")
	fs.StringSliceVar(&o.AlternateDNS, "alternate-dns", o.AlternateDNS, "Specify an alternate DNS to use (e.g. 'onexai.io').")
	fs.DurationVar(&o.EventTTL, "event-ttl", o.EventTTL, "Amount of time to retain events.")

	fs.BoolVar(&o.EnableLogsHandler, "enable-logs-handler", o.EnableLogsHandler,
		"If true, install a /logs handler for the apiserver logs.")
	_ = fs.MarkDeprecated("enable-logs-handler", "This flag will be removed in v1.19")

	fs.StringVar(&o.ProxyClientCertFile, "proxy-client-cert-file", o.ProxyClientCertFile, ""+
		"Client certificate used to prove the identity of the aggregator or kube-apiserver "+
		"when it must call out during a request. This includes proxying requests to a user "+
		"api-server and calling out to webhook admission plugins. It is expected that this "+
		"cert includes a signature from the CA in the --requestheader-client-ca-file flag. "+
		"That CA is published in the 'extension-apiserver-authentication' configmap in "+
		"the kube-system namespace. Components receiving calls from kube-aggregator should "+
		"use that CA to perform their half of the mutual TLS verification.")
	fs.StringVar(&o.ProxyClientKeyFile, "proxy-client-key-file", o.ProxyClientKeyFile, ""+
		"Private key for the client certificate used to prove the identity of the aggregator or kube-apiserver "+
		"when it must call out during a request. This includes proxying requests to a user "+
		"api-server and calling out to webhook admission plugins.")

	fs.StringVar(&o.PeerCAFile, "peer-ca-file", o.PeerCAFile,
		"If set and the UnknownVersionInteroperabilityProxy feature gate is enabled, this file will be used to verify serving certificates of peer kube-apiservers. "+
			"This flag is only used in clusters configured with multiple kube-apiservers for high availability.")

	fs.StringVar(&o.PeerAdvertiseAddress.PeerAdvertiseIP, "peer-advertise-ip", o.PeerAdvertiseAddress.PeerAdvertiseIP,
		"If set and the UnknownVersionInteroperabilityProxy feature gate is enabled, this IP will be used by peer kube-apiservers to proxy requests to this kube-apiserver "+
			"when the request cannot be handled by the peer due to version skew between the kube-apiservers. "+
			"This flag is only used in clusters configured with multiple kube-apiservers for high availability. ")

	fs.StringVar(&o.PeerAdvertiseAddress.PeerAdvertisePort, "peer-advertise-port", o.PeerAdvertiseAddress.PeerAdvertisePort,
		"If set and the UnknownVersionInteroperabilityProxy feature gate is enabled, this port will be used by peer kube-apiservers to proxy requests to this kube-apiserver "+
			"when the request cannot be handled by the peer due to version skew between the kube-apiservers. "+
			"This flag is only used in clusters configured with multiple kube-apiservers for high availability. ")

	fs.BoolVar(&o.EnableAggregatorRouting, "enable-aggregator-routing", o.EnableAggregatorRouting,
		"Turns on aggregator routing requests to the endpoints IP of a Service-backed APIService "+
			"rather than to its cluster IP.")

	fs.BoolVar(&o.AggregatorRejectForwardingRedirects, "aggregator-reject-forwarding-redirect", o.AggregatorRejectForwardingRedirects,
		"Aggregator reject forwarding redirect response back to client.")

	// Authentication and authorization use the kube-apiserver flag sets verbatim,
	// so every documented authentication/authorization extension point has the
	// same name, semantics and validation here. BuiltInAuthorizationOptions also
	// owns the "--authorization-config is mutually exclusive with the legacy
	// authorization flags" check, because only it can see which were set.
	o.Authentication.AddFlags(fss.FlagSet("authentication"))
	o.Authorization.AddFlags(fss.FlagSet("authorization"))
}

func (o *Options) Complete() (CompletedOptions, error) {
	if o == nil {
		return CompletedOptions{completedOptions: &completedOptions{}}, nil
	}

	completed := completedOptions{Options: *o}

	// Fill in the authorizer modes the flag parsing left empty. The
	// authenticator needs no completion of its own: every default (anonymous on,
	// no static tokens, no webhook) lives in the options this struct embeds.
	if errs := completed.Authorization.Complete(); len(errs) != 0 {
		return CompletedOptions{}, fmt.Errorf("error completing authorization options: %w", utilerrors.NewAggregate(errs))
	}

	// set defaults
	if err := completed.GenericServerRunOptions.DefaultAdvertiseAddress(completed.RecommendedOptions.SecureServing.SecureServingOptions); err != nil {
		return CompletedOptions{}, err
	}

	// TODO have a "real" external address
	if err := completed.RecommendedOptions.SecureServing.MaybeDefaultWithSelfSignedCerts(
		completed.GenericServerRunOptions.AdvertiseAddress.String(),
		completed.AlternateDNS,
		[]net.IP{netutils.ParseIPSloppy("127.0.0.1")}); err != nil {
		return CompletedOptions{}, fmt.Errorf("error creating self-signed certificates: %w", err)
	}

	//nolint: nestif
	if len(completed.GenericServerRunOptions.ExternalHost) == 0 {
		if len(completed.GenericServerRunOptions.AdvertiseAddress) > 0 {
			completed.GenericServerRunOptions.ExternalHost = completed.GenericServerRunOptions.AdvertiseAddress.String()
		} else {
			if hostname, err := os.Hostname(); err == nil {
				completed.GenericServerRunOptions.ExternalHost = hostname
			} else {
				return CompletedOptions{}, fmt.Errorf("error finding host name: %w", err)
			}
		}
		klog.Infof("external host was not specified, using %v", completed.GenericServerRunOptions.ExternalHost)
	}

	// UPDATEME: When add authorization and authentication features
	/*
			// put authorization options in final state
		    completed.Authorization.Complete()
		    // adjust authentication for completed authorization
		    completed.Authentication.ApplyAuthorization(completed.Authorization)
	*/

	for key, value := range completed.APIEnablement.RuntimeConfig {
		if key == "v1" || strings.HasPrefix(key, "v1/") ||
			key == "api/v1" || strings.HasPrefix(key, "api/v1/") {
			delete(completed.APIEnablement.RuntimeConfig, key)
			completed.APIEnablement.RuntimeConfig["/v1"] = value
		}
		if key == "api/legacy" {
			delete(completed.APIEnablement.RuntimeConfig, key)
		}
	}

	return CompletedOptions{
		completedOptions: &completed,
	}, nil
}
