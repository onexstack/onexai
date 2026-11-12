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

// Package options holds the flags and configuration of oai-job-controller.
package options

import (
	"errors"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	customoptions "github.com/onexstack/onexai/pkg/options"
)

var (
	errEndpointRequired = errors.New("agent.endpoint is required")
	errAKSKRequired     = errors.New("agent-aksk.api-key and agent-aksk.secret-key are required")
)

// ServerOptions is the configuration of oai-job-controller.
type ServerOptions struct {
	// Kubeconfig points at oai-declserver, which serves batch.onex.io.
	Kubeconfig string `json:"kubeconfig" mapstructure:"kubeconfig"`
	// Master overrides the API server address in Kubeconfig.
	Master string `json:"master" mapstructure:"master"`
	// AgentOptions configures the oai-agent-apiserver client.
	AgentOptions *genericoptions.RestyOptions `json:"agent" mapstructure:"agent"`
	// AgentAKSKOptions is the signed identity used for run submission.
	AgentAKSKOptions *customoptions.AKSKOptions `json:"agent-aksk" mapstructure:"agent-aksk"`
	// JobTypes restricts this instance to the listed provider types. Empty
	// means every type, which is the single-manager deployment. Deploying one
	// instance per type isolates credentials and scaling, like job-aws-vm.
	JobTypes []string `json:"job-types" mapstructure:"job-types"`
	// ProviderPollInterval is how often a running Job re-checks its provider.
	ProviderPollInterval time.Duration `json:"provider-poll-interval" mapstructure:"provider-poll-interval"`
	// MaxConcurrentReconciles bounds the Job controller's worker count.
	MaxConcurrentReconciles int `json:"max-concurrent-reconciles" mapstructure:"max-concurrent-reconciles"`
	// LeaderElection enables manager-level leader election.
	LeaderElection bool `json:"leader-election" mapstructure:"leader-election"`
	// LeaderElectionID is the lease name.
	LeaderElectionID string `json:"leader-election-id" mapstructure:"leader-election-id"`
	// LeaderElectionNamespace is the namespace of the lease.
	LeaderElectionNamespace string `json:"leader-election-namespace" mapstructure:"leader-election-namespace"`
	// MetricsAddr is the metrics bind address.
	MetricsAddr string `json:"metrics-addr" mapstructure:"metrics-addr"`
	// HealthProbeAddr is the health probe bind address.
	HealthProbeAddr string `json:"health-probe-addr" mapstructure:"health-probe-addr"`
}

// NewServerOptions returns options with defaults.
func NewServerOptions() *ServerOptions {
	return &ServerOptions{
		AgentOptions:            genericoptions.NewRestyOptions(),
		AgentAKSKOptions:        customoptions.NewAKSKOptions(),
		ProviderPollInterval:    2 * time.Second,
		MaxConcurrentReconciles: 20,
		LeaderElection:          true,
		LeaderElectionID:        "oai-job-controller",
		MetricsAddr:             ":8080",
		HealthProbeAddr:         ":8081",
	}
}

// AddFlags binds the options to flags.
func (o *ServerOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Kubeconfig, "kubeconfig", o.Kubeconfig, "Path to the kubeconfig of oai-declserver.")
	fs.StringVar(&o.Master, "master", o.Master, "The address of the API server, overriding kubeconfig.")
	o.AgentOptions.AddFlags(fs, "agent")
	o.AgentAKSKOptions.AddFlags(fs, "agent-aksk")
	fs.StringSliceVar(&o.JobTypes, "job-types", o.JobTypes, "Restrict this instance to the listed Job types. Empty means all.")
	fs.DurationVar(&o.ProviderPollInterval, "provider-poll-interval", o.ProviderPollInterval, "How often a running Job re-checks its provider.")
	fs.IntVar(&o.MaxConcurrentReconciles, "max-concurrent-reconciles", o.MaxConcurrentReconciles, "Maximum concurrent Job reconciles.")
	fs.BoolVar(&o.LeaderElection, "leader-elect", o.LeaderElection, "Enable leader election.")
	fs.StringVar(&o.LeaderElectionID, "leader-election-id", o.LeaderElectionID, "Leader election lease name.")
	fs.StringVar(&o.LeaderElectionNamespace, "leader-election-namespace", o.LeaderElectionNamespace, "Leader election lease namespace.")
	fs.StringVar(&o.MetricsAddr, "metrics-addr", o.MetricsAddr, "The address the metrics endpoint binds to.")
	fs.StringVar(&o.HealthProbeAddr, "health-probe-addr", o.HealthProbeAddr, "The address the health probe endpoint binds to.")
}

// Complete completes options after flag parsing.
func (o *ServerOptions) Complete() error { return nil }

// Validate checks the options.
func (o *ServerOptions) Validate() error {
	errs := []error{}
	errs = append(errs, o.AgentOptions.Validate()...)
	errs = append(errs, o.AgentAKSKOptions.Validate()...)
	if o.AgentOptions.Endpoint == "" {
		errs = append(errs, errEndpointRequired)
	}
	if !o.AgentAKSKOptions.Enabled() {
		errs = append(errs, errAKSKRequired)
	}
	return utilerrors.NewAggregate(errs)
}
