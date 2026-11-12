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

package settingstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/pkg/declclient"
)

// TestConfigMapStoreLive checks that a setting edited in the control plane
// reaches the next lookup without a restart, which is the whole reason the
// settings are not in the configuration file.
func TestConfigMapStoreLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	const name = "settings-live"
	configMaps := client.KubeClient.CoreV1().ConfigMaps("default")
	_, err = configMaps.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data:       map[string]string{KeySystemPrompt: "you are on call"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = configMaps.Delete(context.Background(), name, metav1.DeleteOptions{})
	})

	store := New(NewConfigMapSource(client.ConfigMaps("default"), name))

	// The lister is filled by the watch, so the create reaches it a moment after
	// the API call returns.
	require.Eventually(t, func() bool {
		return store.GetString(KeySystemPrompt) == "you are on call"
	}, 15*time.Second, 100*time.Millisecond, "the ConfigMap never reached the informer cache")

	// An edit takes effect on the next lookup: no refresh interval, no restart.
	configMap, err := configMaps.Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	configMap.Data[KeySystemPrompt] = "you are off call"
	_, err = configMaps.Update(ctx, configMap, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return store.GetString(KeySystemPrompt) == "you are off call"
	}, 15*time.Second, 100*time.Millisecond, "the edit never reached the informer cache")

	// A key the ConfigMap does not carry is unset, not empty-with-confidence.
	_, ok := NewConfigMapSource(client.ConfigMaps("default"), name).Lookup("absent")
	assert.False(t, ok, "an absent key must read as unset")

	// Deleting the ConfigMap returns the deployment to its configuration-only
	// defaults.
	require.NoError(t, configMaps.Delete(ctx, name, metav1.DeleteOptions{}))
	require.Eventually(t, func() bool {
		_, ok := NewConfigMapSource(client.ConfigMaps("default"), name).Lookup(KeySystemPrompt)
		return !ok
	}, 15*time.Second, 100*time.Millisecond, "the deleted ConfigMap is still being served")
}
