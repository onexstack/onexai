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

package di

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/pkg/settingstore"
	"github.com/onexstack/onexai/pkg/declclient"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// TestProvideSettingStoreLive checks the wiring the unit test cannot: the
// option names a ConfigMap, the provider resolves it in the control plane, and
// the value an operator wrote is what the agent builder reads.
func TestProvideSettingStoreLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	const name = "oai-agent-settings-live"
	configMaps := client.KubeClient.CoreV1().ConfigMaps(metav1.NamespaceDefault)
	_, err = configMaps.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
		Data:       map[string]string{settingstore.KeySystemPrompt: "you are on call"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = configMaps.Delete(context.Background(), name, metav1.DeleteOptions{})
	})

	// The namespace is left empty on purpose: it is the deployment shape this
	// project runs with, and the provider has to resolve it to default.
	cfg := &Config{ControlPlaneOptions: &customoptions.ControlPlaneOptions{
		Kubeconfig:        kubeconfig,
		SettingsConfigMap: name,
	}}
	store := ProvideSettingStore(cfg, client)
	require.NotNil(t, store, "a declared settings ConfigMap must produce a store")

	require.Eventually(t, func() bool {
		return store.GetString(settingstore.KeySystemPrompt) == "you are on call"
	}, 15*time.Second, 100*time.Millisecond, "the provider never served the ConfigMap value")

	// A deployment that declares no ConfigMap has no settings at all.
	none := ProvideSettingStore(&Config{ControlPlaneOptions: &customoptions.ControlPlaneOptions{
		Kubeconfig: kubeconfig,
	}}, client)
	assert.Nil(t, none)
}
