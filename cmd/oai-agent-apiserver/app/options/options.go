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

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	agent "github.com/onexstack/onexai/internal/agent"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// ServerOptions contains the configuration options for oai-agent-apiserver.
type ServerOptions struct {
	// SecureServingOptions contains the TLS configuration options.
	SecureServingOptions *genericoptions.SecureServingOptions `json:"secure" mapstructure:"secure"`
	// InsecureServingOptions contains the HTTP configuration options.
	InsecureServingOptions *genericoptions.InsecureServingOptions `json:"insecure" mapstructure:"insecure"`
	// CoreDBOptions contains the database configuration (dialect, address, credentials).
	CoreDBOptions *customoptions.CoreDBOptions `json:"coredb" mapstructure:"coredb"`
	// OTelOptions used to specify the otel options.
	OTelOptions *genericoptions.OTelOptions `json:"otel" mapstructure:"otel"`
	// FakeOptions specifies whether to create a fake client.
	FakeOptions *genericoptions.RestyOptions `json:"fake" mapstructure:"fake"`
	// OssOptions specifies whether to create an oss client.
	OssOptions *genericoptions.RestyOptions `json:"oss" mapstructure:"oss"`

	// JWTOptions holds the JWT signing key and expiry.
	JWTOptions *genericoptions.JWTOptions `json:"jwt" mapstructure:"jwt"`
	// LarkOptions holds Lark/Feishu application credentials.
	LarkOptions *customoptions.LarkOptions `json:"lark" mapstructure:"lark"`
	// LLMOptions holds configuration for the OpenAI-compatible LLM backend.
	LLMOptions *customoptions.LLMOptions `json:"llm" mapstructure:"llm"`
	// ModelOptions configures model endpoint storage (API key encryption).
	ModelOptions *customoptions.ModelOptions `json:"model" mapstructure:"model"`
	// AuthOptions holds authentication and authorization configuration.
	AuthOptions *customoptions.AuthOptions `json:"auth" mapstructure:"auth"`
	// AuthzOptions configures the multi-level (Agent/Tool/Resource) authorization subsystem.
	AuthzOptions *customoptions.AuthzOptions `json:"authz" mapstructure:"authz"`
	// StoreOptions selects/configures the AI-resource store backend.
	StoreOptions *customoptions.StoreOptions `json:"store" mapstructure:"store"`
	// ControlPlaneOptions points the service at oai-declserver. Its kubeconfig
	// is the switch: empty means the migrated resources have no source and the
	// rule set is empty (fail-closed).
	ControlPlaneOptions *customoptions.ControlPlaneOptions `json:"controlplane" mapstructure:"controlplane"`
	// RedisOptions configures Redis for agent sessions / cross-pod stop signaling.
	RedisOptions *customoptions.RedisOptions `json:"redis" mapstructure:"redis"`
	// AgentOptions configures the AI agent flow (engine, concurrency, timeouts).
	AgentOptions *customoptions.AgentOptions `json:"agent" mapstructure:"agent"`
	// FeatureOptions gates the os-apiserver alignment switches. Every switch
	// defaults to false and is off until verified in a deployment.
	FeatureOptions *customoptions.FeatureOptions `json:"features" mapstructure:"features"`
	// MediaOptions configures where image bytes are stored and how a remote
	// image is fetched.
	MediaOptions *customoptions.MediaOptions `json:"media" mapstructure:"media"`
	// UsageReportOptions names the ledger this deployment reports finished runs
	// to. Leaving it out means this process reports nowhere, which is what a
	// deployment whose runs all arrive through OneX wants; a deployment with a
	// Lark surface wants it set, or those runs are invisible to the ledger.
	UsageReportOptions *customoptions.UsageReportOptions `json:"usage-report" mapstructure:"usage-report"`
}

// NewServerOptions creates a ServerOptions instance with default values.
func NewServerOptions() *ServerOptions {
	opts := &ServerOptions{
		SecureServingOptions:   genericoptions.NewSecureServingOptions(),
		InsecureServingOptions: genericoptions.NewInsecureServingOptions(),
		CoreDBOptions:          customoptions.NewCoreDBOptions(),
		OTelOptions:            genericoptions.NewOTelOptions(),
		FakeOptions:            genericoptions.NewRestyOptions(),
		OssOptions:             genericoptions.NewRestyOptions(),
		JWTOptions:             genericoptions.NewJWTOptions(),
		LarkOptions:            customoptions.NewLarkOptions(),
		LLMOptions:             customoptions.NewLLMOptions(),
		ModelOptions:           customoptions.NewModelOptions(),
		AuthOptions:            customoptions.NewAuthOptions(),
		AuthzOptions:           customoptions.NewAuthzOptions(),
		StoreOptions:           customoptions.NewStoreOptions(),
		ControlPlaneOptions:    customoptions.NewControlPlaneOptions(),
		RedisOptions:           customoptions.NewRedisOptions(),
		AgentOptions:           customoptions.NewAgentOptions(),
		FeatureOptions:         customoptions.NewFeatureOptions(),
		MediaOptions:           customoptions.NewMediaOptions(),
		UsageReportOptions:     customoptions.NewUsageReportOptions(),
	}
	opts.InsecureServingOptions.Addr = ":8080"

	return opts
}

// AddFlags binds the options in ServerOptions to command-line flags.
func (o *ServerOptions) AddFlags(fs *pflag.FlagSet) {
	o.SecureServingOptions.AddFlags(fs, "secure")
	o.InsecureServingOptions.AddFlags(fs, "insecure")
	o.CoreDBOptions.AddFlags(fs, "coredb")
	o.OTelOptions.AddFlags(fs, "otel")
	o.FakeOptions.AddFlags(fs, "fake")
	o.OssOptions.AddFlags(fs, "oss")
	o.JWTOptions.AddFlags(fs, "jwt")
	o.LarkOptions.AddFlags(fs, "lark")
	o.LLMOptions.AddFlags(fs, "llm")
	o.ModelOptions.AddFlags(fs, "model")
	o.AuthOptions.AddFlags(fs, "auth")
	o.AuthzOptions.AddFlags(fs, "authz")
	o.StoreOptions.AddFlags(fs, "store")
	o.ControlPlaneOptions.AddFlags(fs, "controlplane")
	o.RedisOptions.AddFlags(fs, "redis")
	o.AgentOptions.AddFlags(fs, "agent")
	o.FeatureOptions.AddFlags(fs, "features")
	o.MediaOptions.AddFlags(fs, "media")
	o.UsageReportOptions.AddFlags(fs, "usage-report")
}

// Complete normalizes the options after viper has unmarshalled the config file
// and the flags have been parsed. It is the single place where derived values
// are filled in, so NewServerOptions only has to allocate the sub-structures.
//
// Unlike os-apiserver's Complete, this does not fetch secrets from AWS Secrets
// Manager: onexai reads its configuration from the file and the environment, so
// there is nothing to resolve here beyond guarding the derived fields.
func (o *ServerOptions) Complete() error {
	// A config file that predates the features block leaves the pointer nil;
	// allocate it so Validate and Config can assume a non-nil value. All
	// switches default to false, which is the pre-alignment behaviour.
	if o.FeatureOptions == nil {
		o.FeatureOptions = customoptions.NewFeatureOptions()
	}

	return nil
}

// Validate checks whether the options in ServerOptions are valid.
func (o *ServerOptions) Validate() error {
	errs := []error{}

	// Validate sub-options.
	errs = append(errs, o.SecureServingOptions.Validate()...)
	errs = append(errs, o.InsecureServingOptions.Validate()...)
	errs = append(errs, o.CoreDBOptions.Validate()...)
	errs = append(errs, o.OTelOptions.Validate()...)
	errs = append(errs, o.FakeOptions.Validate()...)
	errs = append(errs, o.OssOptions.Validate()...)
	errs = append(errs, o.JWTOptions.Validate()...)
	errs = append(errs, o.LarkOptions.Validate()...)
	errs = append(errs, o.LLMOptions.Validate()...)
	errs = append(errs, o.ModelOptions.Validate()...)
	errs = append(errs, o.AuthOptions.Validate()...)
	errs = append(errs, o.AuthzOptions.Validate()...)
	errs = append(errs, o.StoreOptions.Validate()...)
	errs = append(errs, o.ControlPlaneOptions.Validate()...)
	errs = append(errs, o.RedisOptions.Validate()...)
	errs = append(errs, o.AgentOptions.Validate()...)
	errs = append(errs, o.FeatureOptions.Validate()...)
	errs = append(errs, o.MediaOptions.Validate()...)
	errs = append(errs, o.UsageReportOptions.Validate()...)

	// A token signed with a key that ships in the repository is forgeable by
	// anyone; refuse to start rather than accept them.
	if err := customoptions.ValidateJWTKey(o.JWTOptions.Key); err != nil {
		errs = append(errs, err)
	}

	// Approval parks a paused run until a human answers it, which is a later
	// request that may be served by another pod. Redis is what makes that
	// possible; on the in-memory store an approval only works for as long as the
	// pod that took the pause stays up and keeps load balancing it there, so a
	// deployment that configures approval without Redis has configured something
	// that will fail intermittently rather than work.
	if len(o.AgentOptions.RequireApproval) > 0 && !o.RedisOptions.Enabled() {
		errs = append(errs, fmt.Errorf(
			"agent.require-approval requires redis: a paused run is resumed by a later request, "+
				"which may be served by another pod",
		))
	}

	return utilerrors.NewAggregate(errs)
}

// Config builds a agent.Config based on ServerOptions.
func (o *ServerOptions) Config() (*agent.Config, error) {
	return &agent.Config{
		SecureServingOptions:     o.SecureServingOptions,
		InsecureServingOptions:   o.InsecureServingOptions,
		CoreDBOptions:            o.CoreDBOptions,
		FakeOptions:              o.FakeOptions,
		OssOptions:               o.OssOptions,
		JWTSecret:                o.JWTOptions.Key,
		WebhookToken:             o.AuthOptions.WebhookToken,
		AuthenticationConfig:     o.AuthOptions.AuthenticationConfig,
		LLMBaseURL:               o.LLMOptions.BaseURL,
		LLMAPIKey:                o.LLMOptions.APIKey,
		LLMModel:                 o.LLMOptions.Model,
		LLMMaxTokens:             o.LLMOptions.MaxTokens,
		LLMAllowPrivateEndpoints: o.LLMOptions.AllowPrivateEndpoints,
		LLMTestTimeout:           o.LLMOptions.TestTimeout,
		ModelAPIKeyEncryptionKey: o.ModelOptions.APIKeyEncryptionKey,
		AnonymousAuth:            o.AuthOptions.AnonymousAuth,
		AuthorizationMode:        o.AuthOptions.AuthorizationMode,
		Addr:                     o.InsecureServingOptions.Addr,
		LarkOptions:              o.LarkOptions,
		StoreOptions:             o.StoreOptions,
		ControlPlaneOptions:      o.ControlPlaneOptions,
		RedisOptions:             o.RedisOptions,
		AgentOptions:             o.AgentOptions,
		AuthzOptions:             o.AuthzOptions,
		BypassAKSK:               o.AuthOptions.BypassAKSK,
		ControlToken:             o.AuthOptions.ControlToken,
		FeatureOptions:           o.FeatureOptions,
		MediaOptions:             o.MediaOptions,
		UsageReportOptions:       o.UsageReportOptions,
	}, nil
}
