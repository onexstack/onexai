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

package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// TestHealthReporterLive checks that the runtime's conditions are applied
// alongside the controller's, not over them.
//
// The endpoint is a local server: a probe of the real internet would make the
// test depend on a third party to answer, which is exactly what a probe test
// must not do.
func TestHealthReporterLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer endpoint.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	_, err = client.KubeClient.CoreV1().Secrets("default").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "health-probe-key", Namespace: "default"},
		Data:       map[string][]byte{"api-key": []byte("good-key")},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.KubeClient.CoreV1().Secrets("default").Delete(context.Background(), "health-probe-key", metav1.DeleteOptions{})
	})

	models := client.Clientset.AgentV1alpha1().Models("default")
	created, err := models.Create(ctx, &agentv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "health-probe-model", Namespace: "default"},
		Spec: agentv1alpha1.ModelSpec{
			CommonSpec:    agentv1alpha1.CommonSpec{Enabled: true},
			Endpoint:      endpoint.URL,
			Model:         "probe",
			CredentialRef: &corev1.SecretKeySelector{Name: "health-probe-key", Key: "api-key"},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = models.Delete(context.Background(), created.Name, metav1.DeleteOptions{})
	})

	// Wait for the controller to publish its own condition first, so the test can
	// prove apply preserved it.
	require.Eventually(t, func() bool {
		latest, err := models.Get(ctx, created.Name, metav1.GetOptions{})
		return err == nil && apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionReady) != nil
	}, 15*time.Second, 100*time.Millisecond, "the controller never published Ready")

	reporter := NewHealthReporter(client, time.Minute, Probers{}, "")

	// Surface the apply error instead of hiding it behind a retry loop.
	require.Eventually(t, func() bool {
		_, err := client.Models().Models("default").Get(created.Name)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "the model never reached the informer cache")
	model, err := client.Models().Models("default").Get(created.Name)
	require.NoError(t, err)
	require.NoError(t, reporter.probeModel(ctx, model), "probing or applying the model status failed")

	// The reporter reads the informer cache, which may lag the write that the
	// controller already observed; retry until the probe has been published.
	require.Eventually(t, func() bool {
		reporter.probeModels(ctx)
		latest, err := models.Get(ctx, created.Name, metav1.GetOptions{})
		return err == nil && apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionReachable) != nil
	}, 15*time.Second, 200*time.Millisecond, "the runtime never published Reachable")

	latest, err := models.Get(ctx, created.Name, metav1.GetOptions{})
	require.NoError(t, err)

	reachable := apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionReachable)
	require.NotNil(t, reachable, "the runtime must publish Reachable")
	assert.Equal(t, metav1.ConditionTrue, reachable.Status)

	authOK := apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionAuthOK)
	require.NotNil(t, authOK, "the runtime must publish AuthOK")
	assert.Equal(t, metav1.ConditionTrue, authOK.Status)

	// The controller's condition must survive the runtime's apply: two field
	// managers, one conditions list.
	ready := apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionReady)
	assert.NotNil(t, ready, "the runtime's apply must not erase the controller's condition")

	assert.NotZero(t, latest.Status.LatencyMillis)
}

// TestWebhookHealthLive checks that an endpoint answering, and one that does
// not, are reported differently.
func TestWebhookHealthLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	answer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer answer.Close()

	// A listener that is closed immediately: the port is refused, which is the
	// unreachable case without depending on an external host.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	hooks := client.Clientset.AgentV1alpha1().Webhooks("default")
	create := func(name, endpoint string) *agentv1alpha1.Webhook {
		hook, err := hooks.Create(ctx, &agentv1alpha1.Webhook{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: agentv1alpha1.WebhookSpec{
				CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
				Events:     []string{"onEnd"},
				Endpoint:   endpoint,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = hooks.Delete(context.Background(), name, metav1.DeleteOptions{}) })
		return hook
	}

	healthy := create("health-live-hook-ok", answer.URL)
	unhealthy := create("health-live-hook-dead", deadURL)

	// The reporter reads the informer cache; wait until both objects are there,
	// otherwise a probe silently skips the one that has not arrived yet.
	for _, hook := range []*agentv1alpha1.Webhook{healthy, unhealthy} {
		name := hook.Name
		require.Eventually(t, func() bool {
			_, err := client.Webhooks().Webhooks("default").Get(name)
			return err == nil
		}, 15*time.Second, 100*time.Millisecond, "webhook %q never reached the lister", name)
	}

	reporter := NewHealthReporter(client, time.Minute, Probers{}, "")
	reporter.probeWebhooks(ctx)

	require.Eventually(t, func() bool {
		latest, err := hooks.Get(ctx, healthy.Name, metav1.GetOptions{})
		return err == nil && apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionEndpointHealthy) != nil
	}, 15*time.Second, 100*time.Millisecond, "the answering endpoint was never reported")

	okHook, err := hooks.Get(ctx, healthy.Name, metav1.GetOptions{})
	require.NoError(t, err)
	okCondition := apimeta.FindStatusCondition(okHook.Status.Conditions, agentv1alpha1.ConditionEndpointHealthy)
	require.NotNil(t, okCondition)
	assert.Equal(t, metav1.ConditionTrue, okCondition.Status)

	require.Eventually(t, func() bool {
		latest, err := hooks.Get(ctx, unhealthy.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		condition := apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionEndpointHealthy)
		return condition != nil && condition.Status == metav1.ConditionFalse
	}, 15*time.Second, 100*time.Millisecond, "the refused endpoint was never reported unreachable")
}
