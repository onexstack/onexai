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

package k8s

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	apiserverapi "k8s.io/apiserver/pkg/apis/apiserver"
	apiserverconfigload "k8s.io/apiserver/pkg/apis/apiserver/load"
	kauthz "k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	authorizationcel "k8s.io/apiserver/pkg/authorization/cel"
	webhookauthorizer "k8s.io/apiserver/plugin/pkg/authorizer/webhook"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// defaultRetryBackoff mirrors kube-apiserver's default for the delegating
// authorization webhook retry loop.
var defaultRetryBackoff = wait.Backoff{
	Duration: 500 * time.Millisecond,
	Factor:   1.5,
	Jitter:   0.2,
	Steps:    5,
}

// LoadAuthorizationConfiguration reads an apiserver.config.k8s.io/v1
// AuthorizationConfiguration through the upstream loader.
//
// The loader returns the internal hub type, which is what the webhook
// authorizer plugin consumes; no local mirror of the schema is involved.
func LoadAuthorizationConfiguration(path string) (*apiserverapi.AuthorizationConfiguration, error) {
	cfg, err := apiserverconfigload.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("load authorization config %q: %w", path, err)
	}
	return cfg, nil
}

// NewAuthorizationChain builds the ordered authorizer chain the configuration
// describes.
//
// The order is kube-apiserver's: the first authorizer that returns a decision
// other than NoOpinion wins. A chain with no authorizers returns nil, which the
// caller treats as "no external authorizer configured".
func NewAuthorizationChain(cfg *apiserverapi.AuthorizationConfiguration) (kauthz.Authorizer, error) {
	if cfg == nil || len(cfg.Authorizers) == 0 {
		return nil, nil
	}

	named := make([]NamedAuthorizer, 0, len(cfg.Authorizers))
	for _, authorizerConfig := range cfg.Authorizers {
		if authorizerConfig.Type != "Webhook" {
			return nil, fmt.Errorf("authorizer %q: type %q is not supported; only Webhook is", authorizerConfig.Name, authorizerConfig.Type)
		}
		if authorizerConfig.Webhook == nil {
			return nil, fmt.Errorf("authorizer %q: webhook configuration is required", authorizerConfig.Name)
		}
		authorizer, err := newWebhookAuthorizer(authorizerConfig.Name, authorizerConfig.Webhook)
		if err != nil {
			return nil, fmt.Errorf("authorizer %q: %w", authorizerConfig.Name, err)
		}
		named = append(named, NamedAuthorizer{
			AuthorizerName: authorizerConfig.Name,
			Authorizer:     authorizer,
		})
	}
	return Union(named...)
}

func newWebhookAuthorizer(name string, wh *apiserverapi.WebhookConfiguration) (kauthz.Authorizer, error) {
	if wh.ConnectionInfo.Type == "" {
		return nil, fmt.Errorf("connectionInfo.type is required")
	}

	restConfig, err := connectionConfig(&wh.ConnectionInfo)
	if err != nil {
		return nil, err
	}
	if wh.Timeout.Duration > 0 {
		restConfig.Timeout = wh.Timeout.Duration
	}

	decisionOnError := kauthz.DecisionNoOpinion
	if wh.FailurePolicy == "Deny" {
		decisionOnError = kauthz.DecisionDeny
	}

	return webhookauthorizer.New(
		restConfig,
		wh.SubjectAccessReviewVersion,
		wh.AuthorizedTTL.Duration,
		wh.UnauthorizedTTL.Duration,
		defaultRetryBackoff,
		decisionOnError,
		wh.MatchConditions,
		name,
		authorizerfactory.NewDelegatingAuthorizerMetrics(),
		authorizationcel.NewDefaultCompiler(),
	)
}

func connectionConfig(info *apiserverapi.WebhookConnectionInfo) (*rest.Config, error) {
	switch info.Type {
	case "KubeConfigFile":
		if info.KubeConfigFile == nil || *info.KubeConfigFile == "" {
			return nil, fmt.Errorf("connectionInfo.kubeConfigFile is required when type is KubeConfigFile")
		}
		cfg, err := clientcmd.BuildConfigFromFlags("", *info.KubeConfigFile)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %q: %w", *info.KubeConfigFile, err)
		}
		return cfg, nil
	case "InClusterConfig":
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
		return cfg, nil
	default:
		return nil, fmt.Errorf("connectionInfo.type %q is not supported", info.Type)
	}
}
