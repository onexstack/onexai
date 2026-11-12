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

package options

import (
	"fmt"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
)

var _ genericoptions.IOptions = (*ControlPlaneOptions)(nil)

// ControlPlaneOptions points a runtime service at the declarative control plane
// (oai-declserver).
//
// The kubeconfig path is the switch: when it is empty the service has no
// control plane to read from, and the resources that have been migrated are
// unavailable — there is deliberately no second, database-backed reader left in
// the code, because two readers are two truths.
type ControlPlaneOptions struct {
	// Kubeconfig is the path to a kubeconfig that authenticates against
	// oai-declserver.
	Kubeconfig string `json:"kubeconfig" mapstructure:"kubeconfig"`

	// Namespace narrows every watch to one namespace. Empty watches all of
	// them, which is what a deployment that serves several namespaces needs.
	Namespace string `json:"namespace" mapstructure:"namespace"`

	// SyncTimeout bounds how long start-up waits for the first cache sync.
	// A control plane that cannot be reached by then fails the start instead of
	// serving requests against an empty rule set.
	SyncTimeout time.Duration `json:"sync-timeout" mapstructure:"sync-timeout"`

	// SettingsConfigMap names the ConfigMap, in Namespace, whose Data holds the
	// deployment's runtime settings (today the global system prompt). Empty means
	// the service has no runtime settings and the configuration file is the only
	// source.
	//
	// It is a ConfigMap rather than a resource of our own because settings are
	// process configuration an operator edits between releases, not desired
	// state: `kubectl edit configmap` is the write path, the watch makes the edit
	// visible without a restart, and there is nothing to reconcile.
	SettingsConfigMap string `json:"settings-configmap" mapstructure:"settings-configmap"`

	// ProbeInterval is how often the runtime re-publishes the live health of the
	// resources it talks to (model reachability today).
	ProbeInterval time.Duration `json:"probe-interval" mapstructure:"probe-interval"`

	// ResyncPeriod is the informer resync interval. Watches deliver every
	// change, so this is a safety net rather than the primary mechanism; it is
	// short enough that a missed event heals within a request-worthy window.
	ResyncPeriod time.Duration `json:"resync-period" mapstructure:"resync-period"`
}

// NewControlPlaneOptions creates the defaults.
func NewControlPlaneOptions() *ControlPlaneOptions {
	return &ControlPlaneOptions{
		SyncTimeout:   30 * time.Second,
		ResyncPeriod:  time.Minute,
		ProbeInterval: time.Minute,
	}
}

// AddFlags registers the flags.
func (o *ControlPlaneOptions) AddFlags(fs *pflag.FlagSet, name string) {
	fs.StringVar(&o.Kubeconfig, name+".kubeconfig", o.Kubeconfig,
		"Path to the kubeconfig of the declarative control plane (oai-declserver). Empty disables control-plane reads.")
	fs.StringVar(&o.Namespace, name+".namespace", o.Namespace,
		"Namespace to watch; empty watches every namespace.")
	fs.DurationVar(&o.SyncTimeout, name+".sync-timeout", o.SyncTimeout,
		"How long start-up waits for the first control-plane cache sync.")
	fs.StringVar(&o.SettingsConfigMap, name+".settings-configmap", o.SettingsConfigMap,
		"ConfigMap in "+name+".namespace whose Data holds the runtime settings; empty disables runtime settings.")
	fs.DurationVar(&o.ProbeInterval, name+".probe-interval", o.ProbeInterval,
		"How often the runtime republishes live health (model reachability).")
	fs.DurationVar(&o.ResyncPeriod, name+".resync-period", o.ResyncPeriod,
		"Informer resync interval; a safety net behind the watch.")
}

// Validate checks the options.
func (o *ControlPlaneOptions) Validate() []error {
	var errs []error
	if o.Kubeconfig != "" && o.SyncTimeout <= 0 {
		errs = append(errs, fmt.Errorf("%s.sync-timeout must be positive", "controlplane"))
	}
	if o.Kubeconfig != "" && o.ProbeInterval <= 0 {
		errs = append(errs, fmt.Errorf("%s.probe-interval must be positive", "controlplane"))
	}
	if o.Kubeconfig != "" && o.ResyncPeriod <= 0 {
		errs = append(errs, fmt.Errorf("%s.resync-period must be positive", "controlplane"))
	}
	return errs
}

// Enabled reports whether a control plane is configured.
func (o *ControlPlaneOptions) Enabled() bool { return o.Kubeconfig != "" }
