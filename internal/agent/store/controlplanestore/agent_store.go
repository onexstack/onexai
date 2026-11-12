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

package controlplanestore

import (
	"context"
	"encoding/json"

	"github.com/onexstack/onexstack/pkg/store/where"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/declcrud"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// agentStore is the agents accessor over the control plane.
type agentStore struct {
	*Store[model.AgentM]
	client    *declclient.Client
	namespace string
}

// Ensure agentStore implements the AgentStore interface.
var _ store.AgentStore = (*agentStore)(nil)

// NewAgentStore builds the agents accessor.
func NewAgentStore(client *declclient.Client, namespace string) *agentStore {
	lister := client.Agents()
	list := func() ([]*agentv1alpha1.Agent, error) { return lister.List(labels.Everything()) }

	return &agentStore{Store: declcrud.NewStore(declcrud.Spec[model.AgentM, *agentv1alpha1.Agent]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.Agent] {
			return client.Clientset.AgentV1alpha1().Agents(namespace)
		},
		GetByKey: getByKey(lister.Agents(namespace).Get, agentFromCR),
		List:     list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.Agent) (model.AgentM, error) {
			return agentFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.AgentM, cr *agentv1alpha1.Agent) error {
			AgentToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.Agent {
			return &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.AgentM) string { return row.Name },
		NameOf: func(row model.AgentM) string { return row.Name },
		Column: agentColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.AgentM) string { return rowNamespace(row.Namespace, namespace) }),
		// The console re-asserts its agent before every message. Writing only
		// when the result actually differs keeps a converged deployment
		// read-only and spares every request the resourceVersion conflict that
		// an unconditional update invites.
		SkipUnchanged: true,
	}), client: client, namespace: namespace}
}

// Create writes the agent and then publishes its A2A credential Secret.
//
// The Secret is a side effect of the same request: a caller that supplied a
// credential and got a successful create must be able to call the agent
// immediately, without a second round trip to a "save credential" endpoint.
func (s *agentStore) Create(ctx context.Context, row *model.AgentM) error {
	if err := s.Store.Create(ctx, row); err != nil {
		return err
	}
	return s.syncA2ACredentials(ctx, row)
}

// Update writes the agent and then reconciles its A2A credential Secret.
func (s *agentStore) Update(ctx context.Context, row *model.AgentM) error {
	if err := s.Store.Update(ctx, row); err != nil {
		return err
	}
	return s.syncA2ACredentials(ctx, row)
}

// syncA2ACredentials writes or clears <name>-a2a-credentials.
//
// Empty credential fields mean "keep what is there": the console's edit path
// reads the row (which never carries secret material) and writes it back, so
// treating empty as "clear" would erase the credential on every unrelated edit.
func (s *agentStore) syncA2ACredentials(ctx context.Context, row *model.AgentM) error {
	// Tests build the store without a control-plane client; a store that cannot
	// reach the Secret API is not a credential failure for an agent that has no
	// A2A execution at all.
	if s.client == nil || s.client.KubeClient == nil {
		return nil
	}
	var execution model.AgentExecutionM
	if row.ExecutionJSON == "" {
		return s.clearA2ACredentials(ctx, row)
	}
	if err := json.Unmarshal([]byte(row.ExecutionJSON), &execution); err != nil {
		return nil
	}
	if execution.A2A == nil || execution.A2A.Auth == nil {
		return s.clearA2ACredentials(ctx, row)
	}
	auth := execution.A2A.Auth
	if auth.ClearCredentials || auth.Type == "" || auth.Type == "none" || auth.Type == "runtime" {
		return s.clearA2ACredentials(ctx, row)
	}

	data := make(map[string][]byte)
	switch auth.Type {
	case "apiKey":
		if auth.Credential == "" {
			return nil
		}
		data["apiKey"] = []byte(auth.Credential)
	case "bearer":
		if auth.Credential == "" {
			return nil
		}
		data["token"] = []byte(auth.Credential)
	case "headers":
		if auth.Credential == "" {
			return nil
		}
		data["headers"] = []byte(auth.Credential)
	case "oauth2":
		if auth.ClientID == "" && auth.ClientSecret == "" {
			return nil
		}
		if auth.ClientID != "" {
			data["clientID"] = []byte(auth.ClientID)
		}
		if auth.ClientSecret != "" {
			data["clientSecret"] = []byte(auth.ClientSecret)
		}
	case "aksk":
		if auth.Credential == "" {
			return nil
		}
		var pair struct {
			AccessKey string `json:"accessKey"`
			SecretKey string `json:"secretKey"`
		}
		if err := json.Unmarshal([]byte(auth.Credential), &pair); err != nil {
			return nil
		}
		if pair.AccessKey != "" {
			data["accessKey"] = []byte(pair.AccessKey)
		}
		if pair.SecretKey != "" {
			data["secretKey"] = []byte(pair.SecretKey)
		}
	default:
		return nil
	}
	if len(data) == 0 {
		return nil
	}

	ns := rowNamespace(row.Namespace, s.namespace)
	cr, err := s.client.Clientset.AgentV1alpha1().Agents(ns).Get(ctx, row.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return writeSecret(ctx, s.client.KubeClient, ns, row.Name+"-a2a-credentials", data, "Agent", cr.Name, cr.UID)
}

// A2ACredentials implements store.AgentStore. A missing Secret is (nil, nil):
// an unauthenticated endpoint is a valid configuration.
func (s *agentStore) A2ACredentials(ctx context.Context, name string) (map[string][]byte, error) {
	if s.client == nil || s.client.KubeClient == nil {
		return nil, nil
	}
	row, err := s.Store.Get(ctx, where.F("name", name))
	if err != nil {
		return nil, err
	}
	ns := rowNamespace(row.Namespace, s.namespace)

	// auth.type=runtime delegates the credential to the RuntimeProfile. The
	// agent's own Secret is deliberately absent in that case.
	var execution model.AgentExecutionM
	if row.ExecutionJSON != "" && json.Unmarshal([]byte(row.ExecutionJSON), &execution) == nil &&
		execution.A2A != nil && execution.A2A.Auth != nil && execution.A2A.Auth.Type == "runtime" &&
		execution.A2A.RuntimeRef != "" {
		profile, err := s.client.Clientset.AgentV1alpha1().RuntimeProfiles(ns).Get(
			ctx, execution.A2A.RuntimeRef, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if profile.Spec.Auth != nil && profile.Spec.Auth.BearerTokenRef != nil {
			ref := profile.Spec.Auth.BearerTokenRef
			secret, err := s.client.KubeClient.CoreV1().Secrets(profile.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return map[string][]byte{"token": append([]byte(nil), secret.Data[ref.Key]...)}, nil
		}
		return nil, nil
	}

	secret, err := s.client.KubeClient.CoreV1().Secrets(ns).Get(ctx, name+"-a2a-credentials", metav1.GetOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make(map[string][]byte, len(secret.Data))
	for key, value := range secret.Data {
		out[key] = append([]byte(nil), value...)
	}
	return out, nil
}

func (s *agentStore) clearA2ACredentials(ctx context.Context, row *model.AgentM) error {
	if s.client == nil || s.client.KubeClient == nil {
		return nil
	}
	ns := rowNamespace(row.Namespace, s.namespace)
	return ignoreNotFound(s.client.KubeClient.CoreV1().Secrets(ns).Delete(
		ctx, row.Name+"-a2a-credentials", metav1.DeleteOptions{}))
}

// agentColumn reads a filterable column off an agent row.
func agentColumn(row model.AgentM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "workspace", "namespace":
		return row.Namespace, true
	case "owner":
		return row.Owner, true
	default:
		return "", false
	}
}
