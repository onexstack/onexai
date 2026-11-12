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

package conversion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestWebhookClientConfigRoundTrip(t *testing.T) {
	in := &v1.Webhook{
		Name: "hook",
		ClientConfig: &v1.ClientConfig{
			Url:            "https://example.com/hook",
			TimeoutSeconds: 5,
			Headers:        map[string]string{"X-Tenant": "onexai"},
		},
	}

	model := WebhookV1ToWebhookModel(in)
	require.NotEmpty(t, model.ClientConfigJSON)
	assert.Equal(t, "https://example.com/hook", model.URL)
	assert.Equal(t, 5, model.TimeoutSeconds)
	assert.Equal(t, "onexai", model.Headers["X-Tenant"])

	out := WebhookModelToWebhookV1(model)
	require.NotNil(t, out.ClientConfig)
	assert.Equal(t, "https://example.com/hook", out.ClientConfig.Url)
	assert.Equal(t, int32(5), out.ClientConfig.TimeoutSeconds)
	assert.Equal(t, "onexai", out.ClientConfig.Headers["X-Tenant"])
}

func TestWebhookClientConfigServiceReference(t *testing.T) {
	in := &v1.Webhook{
		Name: "hook",
		ClientConfig: &v1.ClientConfig{
			Service: &v1.ServiceReference{
				Namespace: "onexai",
				Name:      "hook",
				Port:      8443,
				Path:      "/deliver",
			},
		},
	}

	model := WebhookV1ToWebhookModel(in)
	assert.Empty(t, model.URL, "a Service-based hook has no top-level URL")

	out := WebhookModelToWebhookV1(model)
	require.NotNil(t, out.ClientConfig)
	require.NotNil(t, out.ClientConfig.Service)
	assert.Equal(t, "onexai", out.ClientConfig.Service.Namespace)
	assert.Equal(t, "hook", out.ClientConfig.Service.Name)
	assert.Equal(t, int32(8443), out.ClientConfig.Service.Port)
	assert.Equal(t, "/deliver", out.ClientConfig.Service.Path)
}

func TestWebhookAuthTokenIsWriteOnly(t *testing.T) {
	in := &v1.Webhook{
		Name:      "hook",
		Url:       "https://example.com/hook",
		AuthToken: "paste-me",
	}

	model := WebhookV1ToWebhookModel(in)
	assert.Equal(t, "paste-me", model.AuthToken)

	out := WebhookModelToWebhookV1(model)
	assert.Empty(t, out.AuthToken, "a write-only credential must never be echoed")
}

func TestWebhookClientConfigSecretRefNoLongerWins(t *testing.T) {
	// The top-level secretRef is the only credential reference. A stale nested
	// value must not override the one the operator just submitted.
	in := &v1.Webhook{
		Name:      "hook",
		SecretRef: "chosen",
		ClientConfig: &v1.ClientConfig{
			Url:       "https://example.com/hook",
			SecretRef: "stale",
		},
	}

	model := WebhookV1ToWebhookModel(in)
	assert.Equal(t, "chosen", model.SecretRef)
}
