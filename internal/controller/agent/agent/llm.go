// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/controller/support"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// aggregationModel is the resolved endpoint used to merge prompts.
type aggregationModel struct {
	Name           string
	Endpoint       string
	APIKey         string
	Model          string
	Provider       string
	Timeout        time.Duration
	MaxTokens      int
	MaxInputTokens int
}

// identity is the part of the endpoint that changes what the model would
// produce. The resourceVersion is deliberately excluded: the Model controller
// writes status on every probe, and that must not trigger re-aggregation.
func (m aggregationModel) identity() string {
	return m.Name + "|" + m.Endpoint + "|" + m.Model + "|" + m.Provider
}

func (m aggregationModel) chatModel() *chatmodel.OpenAIChatModel {
	if m.Endpoint == "" {
		return nil
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return chatmodel.NewOpenAIChatModel(m.Endpoint, m.APIKey, m.Model).
		WithHTTPClient(&http.Client{Timeout: timeout}).
		WithMaxTokens(m.MaxTokens).
		WithRetry("", 2, time.Second)
}

// resolveAggregationModel applies the agent's modelRef first and the
// controller's configured default second. A condition is returned instead of
// silently falling back when the selected endpoint is broken.
func (r *Reconciler) resolveAggregationModel(ctx context.Context, agent *agentv1alpha1.Agent) (aggregationModel, metav1.Condition, error) {
	cfg := support.PromptAggregation()
	name := ""
	namespace := agent.Namespace
	if agent.Spec.ModelRef != nil {
		name = agent.Spec.ModelRef.Name
		if agent.Spec.ModelRef.Namespace != "" {
			namespace = agent.Spec.ModelRef.Namespace
		}
	} else if cfg.Model != "" {
		name = cfg.Model
	}
	if name == "" {
		return aggregationModel{}, promptCondition(agent, false, "ModelMissing",
			"no modelRef and promptAggregation.model is empty"), nil
	}

	model := &agentv1alpha1.Model{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, model); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return aggregationModel{}, metav1.Condition{}, err
		}
		return aggregationModel{}, promptCondition(agent, false, "ModelMissing",
			fmt.Sprintf("model %q does not exist in %s", name, namespace)), nil
	}
	if !model.Spec.Enabled {
		return aggregationModel{}, promptCondition(agent, false, "ModelDisabled",
			fmt.Sprintf("model %q is disabled", name)), nil
	}
	if model.Spec.Endpoint == "" || model.Spec.Model == "" {
		return aggregationModel{}, promptCondition(agent, false, "ModelIncomplete",
			fmt.Sprintf("model %q must declare endpoint and model", name)), nil
	}

	apiKey := ""
	if ref := model.Spec.CredentialRef; ref != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, secret); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return aggregationModel{}, metav1.Condition{}, err
			}
			return aggregationModel{}, promptCondition(agent, false, "CredentialMissing",
				fmt.Sprintf("credential Secret %q does not exist in %s", ref.Name, namespace)), nil
		}
		apiKey = string(secret.Data[ref.Key])
		if apiKey == "" {
			return aggregationModel{}, promptCondition(agent, false, "CredentialMissing",
				fmt.Sprintf("credential Secret %q has no key %q", ref.Name, ref.Key)), nil
		}
		// A Model written through the API stores its key sealed. Sending that
		// value upstream would be rejected as a bad credential, so an
		// unopenable one is reported as the configuration problem it is.
		if agentmodel.IsSealedAPIKey(apiKey) {
			key, err := agentmodel.OpenAPIKey(apiKey, cfg.ModelAPIKeyEncryptionKey)
			if err != nil {
				return aggregationModel{}, promptCondition(agent, false, "CredentialEncrypted",
					fmt.Sprintf("credential Secret %q is sealed and cannot be opened; "+
						"promptAggregation.modelAPIKeyEncryptionKey must match the key the agent-apiserver sealed it with",
						ref.Name)), nil
			}
			apiKey = key
		}
	}

	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4000
	}
	maxInputTokens := cfg.MaxInputTokens
	if maxInputTokens <= 0 {
		maxInputTokens = 60000
	}
	return aggregationModel{
		Name:           name,
		Endpoint:       model.Spec.Endpoint,
		APIKey:         apiKey,
		Model:          model.Spec.Model,
		Provider:       model.Spec.Provider,
		Timeout:        cfg.Timeout,
		MaxTokens:      maxTokens,
		MaxInputTokens: maxInputTokens,
	}, metav1.Condition{}, nil
}
