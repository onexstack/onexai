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

// Package options provides the flags used for the controller manager.
package options

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/tools/clientcmd"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"
	"k8s.io/component-base/metrics"
	"k8s.io/kubernetes/pkg/controller/garbagecollector"

	"k8s.io/client-go/kubernetes"

	controllermanagerconfig "github.com/onexstack/onexai/cmd/oai-controller-manager/app/config"
	"github.com/onexstack/onexai/cmd/oai-controller-manager/names"
	ctrlmgrconfig "github.com/onexstack/onexai/internal/controller/apis/config"
	"github.com/onexstack/onexai/internal/controller/apis/config/latest"
	controllervalidation "github.com/onexstack/onexai/internal/controller/apis/config/validation"
	clientcmdutil "github.com/onexstack/onexai/internal/pkg/util/clientcmd"
	kubeutil "github.com/onexstack/onexai/internal/pkg/util/kube"
	genericconfig "github.com/onexstack/onexai/pkg/config"
	genericconfigoptions "github.com/onexstack/onexai/pkg/config/options"
)

const (
	// ControllerManagerUserAgent is the userAgent name when starting onex-controller managers.
	ControllerManagerUserAgent = "oai-controller-manager"
)

// Options is the main context object for the onex-controller manager.
type Options struct {
	Generic                    *genericconfigoptions.GenericControllerManagerConfigurationOptions
	GarbageCollectorController *genericconfigoptions.GarbageCollectorControllerOptions
	CoreDB                     *genericconfigoptions.CoreDBOptions
	Mirror                     *MirrorOptions
	// NamespaceController        *NamespaceControllerOptions

	// ConfigFile is the location of the miner controller server's configuration file.
	ConfigFile string

	// WriteConfigTo is the path where the default configuration will be written.
	WriteConfigTo string

	// The address of the Kubernetes API server (overrides any value in kubeconfig).
	Master string
	// Path to kubeconfig file with authorization and master location information.
	Kubeconfig string
	Metrics    *metrics.Options
	Logs       *logs.Options

	// config is the onex controller manager server's configuration object.
	// The default values.
	config *ctrlmgrconfig.OneXControllerManagerConfiguration
}

// NewOptions creates a new Options with a default config.
func NewOptions() (*Options, error) {
	componentConfig, err := latest.Default()
	if err != nil {
		return nil, err
	}

	o := Options{
		Generic:                    genericconfigoptions.NewGenericControllerManagerConfigurationOptions(&componentConfig.Generic),
		GarbageCollectorController: genericconfigoptions.NewGarbageCollectorControllerOptions(&componentConfig.GarbageCollectorController),
		CoreDB:                     genericconfigoptions.NewCoreDBOptions(&componentConfig.CoreDB),
		Mirror:                     NewMirrorOptions(&componentConfig.Mirror),
		Kubeconfig:                 clientcmdutil.DefaultKubeconfig(),
		Metrics:                    metrics.NewOptions(),
		Logs:                       logs.NewOptions(),
		// config:     componentConfig,
	}

	gcIgnoredResources := make([]genericconfig.GroupResource, 0, len(garbagecollector.DefaultIgnoredResources()))
	for r := range garbagecollector.DefaultIgnoredResources() {
		gcIgnoredResources = append(gcIgnoredResources, genericconfig.GroupResource{Group: r.Group, Resource: r.Resource})
	}
	o.GarbageCollectorController.GCIgnoredResources = gcIgnoredResources
	o.Generic.LeaderElection.ResourceName = "oai-controller-manager"
	o.Generic.LeaderElection.ResourceNamespace = metav1.NamespaceSystem

	return &o, nil
}

// Flags returns flags for a specific APIServer by section name.
func (o *Options) Flags(allControllers []string, disabledControllers []string, controllerAliases map[string]string) cliflag.NamedFlagSets {
	fss := cliflag.NamedFlagSets{}
	o.Generic.AddFlags(&fss, allControllers, disabledControllers, controllerAliases)
	o.GarbageCollectorController.AddFlags(fss.FlagSet(names.GarbageCollectorController))
	o.CoreDB.AddFlags(fss.FlagSet("coredb"))
	o.Mirror.AddFlags(fss.FlagSet("mirror"))

	o.Metrics.AddFlags(fss.FlagSet("metrics"))
	logsapi.AddFlags(o.Logs, fss.FlagSet("logs"))

	fs := fss.FlagSet("misc")
	fs.StringVar(&o.ConfigFile, "config", o.ConfigFile, "The path to the configuration file.")
	fs.StringVar(&o.WriteConfigTo, "write-config-to", o.WriteConfigTo, "If set, write the default configuration values to this file and exit.")
	fs.StringVar(&o.Master, "master", o.Master, "The address of the Kubernetes API server (overrides any value in kubeconfig).")
	fs.StringVar(&o.Kubeconfig, "kubeconfig", o.Kubeconfig, "Path to kubeconfig file with authorization and master location information.")

	utilfeature.DefaultMutableFeatureGate.AddFlag(fss.FlagSet("generic"))

	return fss
}

// Complete completes all the required options. When --config is given the file
// is the source of the component configuration and the section flags are not
// applied on top of it, which is the behaviour kube-controller-manager has.
func (o *Options) Complete() error {
	if o.ConfigFile == "" {
		return nil
	}
	cfg, err := LoadConfigFromFile(o.ConfigFile)
	if err != nil {
		return err
	}
	o.config = cfg
	return nil
}

// ApplyTo fills up onex controller manager config with options.
func (o *Options) ApplyTo(c *controllermanagerconfig.Config, allControllers []string, disabledControllers []string, controllerAliases map[string]string) error {
	if o.config != nil {
		c.ComponentConfig = o.config
	} else {
		if err := o.Generic.ApplyTo(&c.ComponentConfig.Generic, allControllers, disabledControllers, controllerAliases); err != nil {
			return err
		}
		if err := o.GarbageCollectorController.ApplyTo(&c.ComponentConfig.GarbageCollectorController); err != nil {
			return err
		}
		if err := o.CoreDB.ApplyTo(&c.ComponentConfig.CoreDB); err != nil {
			return err
		}
		if err := o.Mirror.ApplyTo(&c.ComponentConfig.Mirror); err != nil {
			return err
		}
	}

	ctrlmgrconfig.RecommendedDefaultPromptAggregationConfiguration(&c.ComponentConfig.PromptAggregation)
	ctrlmgrconfig.RecommendedDefaultMirrorConfiguration(&c.ComponentConfig.Mirror)
	o.Metrics.Apply()

	return nil
}

// Validate is used to validate the options and config before launching the controller.
func (o *Options) Validate(allControllers []string, disabledControllers []string, controllerAliases map[string]string) error {
	var errs []error

	errs = append(errs, o.Generic.Validate(allControllers, disabledControllers, controllerAliases)...)
	errs = append(errs, o.GarbageCollectorController.Validate()...)
	errs = append(errs, o.CoreDB.Validate()...)
	errs = append(errs, o.Mirror.Validate()...)

	// TODO: validate component config, master and kubeconfig

	return utilerrors.NewAggregate(errs)
}

// Config return a controller manager config objective.
func (o Options) Config(allControllers []string, disabledControllers []string, controllerAliases map[string]string) (*controllermanagerconfig.Config, error) {
	kubeconfig, err := clientcmd.BuildConfigFromFlags(o.Master, o.Kubeconfig)
	if err != nil {
		return nil, err
	}
	kubeconfig.DisableCompression = true

	restConfig := kubeutil.AddUserAgent(kubeconfig, ControllerManagerUserAgent)

	c := &controllermanagerconfig.Config{
		ComponentConfig: &ctrlmgrconfig.OneXControllerManagerConfiguration{},
	}

	if err := o.ApplyTo(c, allControllers, disabledControllers, controllerAliases); err != nil {
		return nil, err
	}

	// Validate the configuration that will actually be used. Options.Validate only
	// sees the CLI options, so a value coming from --config used to reach the
	// controllers unchecked.
	if errs := controllervalidation.Validate(c.ComponentConfig); len(errs) > 0 {
		return nil, errs.ToAggregate()
	}

	// The client rate limits come from the component config, so the client has to
	// be built after ApplyTo rather than before.
	clientConnection := c.ComponentConfig.Generic.ClientConnection
	restConfig.QPS = clientConnection.QPS
	restConfig.Burst = int(clientConnection.Burst)
	if clientConnection.ContentType != "" {
		restConfig.ContentConfig.ContentType = clientConnection.ContentType
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	c.Client = client
	c.Kubeconfig = kubeutil.SetClientOptionsForController(restConfig)

	return c, nil
}
