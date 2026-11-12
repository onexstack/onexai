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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kauthz "k8s.io/apiserver/pkg/authorization/authorizer"
)

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
  - name: test
    cluster:
      server: https://127.0.0.1:1
      insecure-skip-tls-verify: true
users:
  - name: test
    user:
      token: test
contexts:
  - name: test
    context:
      cluster: test
      user: test
current-context: test
`

func writeTestKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "webhook.kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(testKubeconfig), 0o600))
	return path
}

func authorizationConfig(t *testing.T, failurePolicy string) string {
	t.Helper()
	kubeconfig := writeTestKubeconfig(t)
	content := `apiVersion: apiserver.config.k8s.io/v1
kind: AuthorizationConfiguration
authorizers:
  - type: Webhook
    name: test-pdp
    webhook:
      authorizedTTL: 1m
      unauthorizedTTL: 1s
      timeout: 100ms
      subjectAccessReviewVersion: v1
      matchConditionSubjectAccessReviewVersion: v1
      failurePolicy: ` + failurePolicy + `
      connectionInfo:
        type: KubeConfigFile
        kubeConfigFile: ` + kubeconfig + `
`
	path := filepath.Join(t.TempDir(), "authorization-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadAuthorizationConfiguration(t *testing.T) {
	cfg, err := LoadAuthorizationConfiguration(authorizationConfig(t, "NoOpinion"))
	require.NoError(t, err)
	require.Len(t, cfg.Authorizers, 1)
	assert.Equal(t, "test-pdp", cfg.Authorizers[0].Name)
}

func TestLoadAuthorizationConfigurationRejectsAMissingFile(t *testing.T) {
	_, err := LoadAuthorizationConfiguration(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}

func TestNewAuthorizationChainHonorsFailurePolicy(t *testing.T) {
	for _, tt := range []struct {
		policy string
		want   kauthz.Decision
	}{
		{policy: "NoOpinion", want: kauthz.DecisionNoOpinion},
		{policy: "Deny", want: kauthz.DecisionDeny},
	} {
		t.Run(tt.policy, func(t *testing.T) {
			cfg, err := LoadAuthorizationConfiguration(authorizationConfig(t, tt.policy))
			require.NoError(t, err)
			chain, err := NewAuthorizationChain(cfg)
			require.NoError(t, err)
			require.NotNil(t, chain)

			decision, _, _ := chain.Authorize(context.Background(), &kauthz.AttributesRecord{})
			assert.Equal(t, tt.want, decision)
		})
	}
}

func TestNewAuthorizationChainRejectsANonWebhookAuthorizer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorization-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: apiserver.config.k8s.io/v1
kind: AuthorizationConfiguration
authorizers:
  - type: Node
    name: nope
`), 0o600))

	cfg, err := LoadAuthorizationConfiguration(path)
	if err != nil {
		// The upstream validation may reject the type before it reaches us;
		// either layer refusing is the correct outcome.
		return
	}
	_, err = NewAuthorizationChain(cfg)
	assert.Error(t, err)
}
