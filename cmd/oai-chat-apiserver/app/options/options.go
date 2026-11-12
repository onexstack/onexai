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
	"os"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	chat "github.com/onexstack/onexai/internal/chat"
	"github.com/onexstack/onexai/internal/pkg/oidc"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// ServerOptions contains the configuration options for oai-chat-apiserver.
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
	// AgentOptions configures the oai-agent-apiserver client.
	AgentOptions *genericoptions.RestyOptions `json:"agent" mapstructure:"agent"`
	// AgentAKSKOptions is the AK/SK pair this server signs its
	// oai-agent-apiserver requests with. oai-agent-apiserver authenticates every /v1 call,
	// so an unset pair means every agent call is refused.
	AgentAKSKOptions *customoptions.AKSKOptions `json:"agent-aksk" mapstructure:"agent-aksk"`
	// AuthOptions holds authentication and authorization configuration.
	AuthOptions *customoptions.AuthOptions `json:"auth" mapstructure:"auth"`
	// BackendOptions configures the auxiliary HTTP-backed clients.
	BackendOptions *customoptions.BackendOptions `json:"backend" mapstructure:"backend"`
	// DutyOptions selects the on-call/incident provider.
	DutyOptions *customoptions.DutyOptions `json:"duty" mapstructure:"duty"`
	// ChatOptions configures session lifecycle, streaming, and the agent
	// backends.
	ChatOptions *customoptions.ChatOptions `json:"chat" mapstructure:"chat"`
	// MCPOptions configures the MCP listener that publishes this service's
	// commands to oai-agent-apiserver's agents.
	MCPOptions *customoptions.MCPOptions `json:"mcp" mapstructure:"mcp"`
	// FeatureOptions gates the os-apiserver alignment switches. Every switch
	// defaults to false and is off until verified in a deployment.
	FeatureOptions *customoptions.FeatureOptions `json:"features" mapstructure:"features"`
	// ControlPlaneOptions points the service at oai-declserver.
	ControlPlaneOptions *customoptions.ControlPlaneOptions `json:"controlplane" mapstructure:"controlplane"`
	// StoreOptions selects how the control-plane resources are read: the informer
	// cache, or the PostgreSQL projection.
	StoreOptions *customoptions.StoreOptions `json:"store" mapstructure:"store"`
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
		AgentOptions:           genericoptions.NewRestyOptions(),
		AgentAKSKOptions:       customoptions.NewAKSKOptions(),
		AuthOptions:            customoptions.NewAuthOptions(),
		BackendOptions:         customoptions.NewBackendOptions(),
		DutyOptions:            customoptions.NewDutyOptions(),
		ChatOptions:            customoptions.NewChatOptions(),
		MCPOptions:             customoptions.NewMCPOptions(),
		FeatureOptions:         customoptions.NewFeatureOptions(),
		ControlPlaneOptions:    customoptions.NewControlPlaneOptions(),
		StoreOptions:           customoptions.NewStoreOptions(),
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
	o.AgentOptions.AddFlags(fs, "agent")
	o.AgentAKSKOptions.AddFlags(fs, "agent-aksk")
	o.AuthOptions.AddFlags(fs, "auth")
	o.BackendOptions.AddFlags(fs, "backend")
	o.DutyOptions.AddFlags(fs, "duty")
	o.ChatOptions.AddFlags(fs, "chat")
	o.MCPOptions.AddFlags(fs, "mcp")
	o.FeatureOptions.AddFlags(fs, "features")
	o.ControlPlaneOptions.AddFlags(fs, "controlplane")
	o.StoreOptions.AddFlags(fs, "store")
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
	errs = append(errs, o.AuthOptions.Validate()...)
	errs = append(errs, o.AgentAKSKOptions.Validate()...)
	errs = append(errs, o.ChatOptions.Validate()...)
	errs = append(errs, o.MCPOptions.Validate()...)
	errs = append(errs, o.FeatureOptions.Validate()...)
	errs = append(errs, o.ControlPlaneOptions.Validate()...)
	errs = append(errs, o.StoreOptions.Validate()...)

	// A token signed with a key that ships in the repository is forgeable by
	// anyone; refuse to start rather than issue them.
	if err := customoptions.ValidateJWTKey(o.JWTOptions.Key); err != nil {
		errs = append(errs, err)
	}

	// The local agent-CLI backend lets a chat message run a process on this
	// host. Combined with auth that admits anonymous or every request, that is
	// remote code execution reachable from the network. Refuse to start rather
	// than warn: by the time a request arrives, the misconfiguration looks
	// exactly like a working setup.
	if o.ChatOptions.Agents.CLI.Enabled {
		if o.AuthOptions.AnonymousAuth {
			errs = append(errs, fmt.Errorf(
				"chat.agents.cli.enabled requires auth.anonymous-auth=false: "+
					"the CLI backend runs local processes and must not be reachable anonymously"))
		}
		if o.AuthOptions.AuthorizationMode == "AlwaysAllow" {
			errs = append(errs, fmt.Errorf(
				"chat.agents.cli.enabled requires auth.authorization-mode != AlwaysAllow: "+
					"the CLI backend runs local processes and must not be reachable without authorisation"))
		}
	}

	return utilerrors.NewAggregate(errs)
}

// Config builds a chat.Config based on ServerOptions.
func (o *ServerOptions) Config() (*chat.Config, error) {
	oidcSigner, err := oidcSigner(o.ChatOptions.OIDC)
	if err != nil {
		return nil, err
	}
	return &chat.Config{
		SecureServingOptions:   o.SecureServingOptions,
		InsecureServingOptions: o.InsecureServingOptions,
		CoreDBOptions:          o.CoreDBOptions,
		FakeOptions:            o.FakeOptions,
		OssOptions:             o.OssOptions,
		JWTSecret:              o.JWTOptions.Key,
		OIDCSigner:             oidcSigner,
		AnonymousAuth:          o.AuthOptions.AnonymousAuth,
		AuthorizationMode:      o.AuthOptions.AuthorizationMode,
		LarkOptions:            o.LarkOptions,
		AgentOptions:           o.AgentOptions,
		AgentAPIKey:            o.AgentAKSKOptions.APIKey,
		AgentSecretKey:         o.AgentAKSKOptions.SecretKey,
		BackendOptions:         o.BackendOptions,
		DutyOptions:            o.DutyOptions,
		ChatOptions:            o.ChatOptions,
		MCPOptions:             o.MCPOptions,
		FeatureOptions:         o.FeatureOptions,
		ControlPlaneOptions:    o.ControlPlaneOptions,
		StoreOptions:           o.StoreOptions,
	}, nil
}

// oidcSigner builds the OIDC issuer, or returns nil when the deployment has not
// enabled it. A configured but unreadable key is a startup error: silently
// falling back to an ephemeral key would invalidate every token on the next
// restart without anyone noticing.
func oidcSigner(opts customoptions.OIDCOptions) (*oidc.Signer, error) {
	if !opts.Enabled {
		return nil, nil
	}
	var keyPEM []byte
	if opts.KeyFile != "" {
		data, err := os.ReadFile(opts.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("read oidc key file %q: %w", opts.KeyFile, err)
		}
		keyPEM = data
	}
	return oidc.New(oidc.Config{
		IssuerURL:     opts.IssuerURL,
		Audiences:     opts.Audiences,
		KeyID:         opts.KeyID,
		PrivateKeyPEM: keyPEM,
		TokenTTL:      opts.TokenTTL,
	})
}
