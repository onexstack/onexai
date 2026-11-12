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

package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func TestValidateWebhookRequiresAnEndpoint(t *testing.T) {
	spec := &agent.WebhookSpec{Events: []string{"onStart"}}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.NotEmpty(t, errs)
}

func TestValidateWebhookAcceptsClientConfigURL(t *testing.T) {
	spec := &agent.WebhookSpec{
		Events: []string{"onStart"},
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL: stringPtr("https://example.com/hook"),
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.Empty(t, errs)
}

func TestValidateWebhookRejectsURLAndServiceTogether(t *testing.T) {
	spec := &agent.WebhookSpec{
		Events: []string{"onStart"},
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL:     stringPtr("https://example.com/hook"),
				Service: &admissionregistrationv1.ServiceReference{Namespace: "ns", Name: "svc"},
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.NotEmpty(t, errs)
}

func TestValidateWebhookRejectsAnIncompleteService(t *testing.T) {
	spec := &agent.WebhookSpec{
		Events: []string{"onStart"},
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{},
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.NotEmpty(t, errs)
}

func TestValidateWebhookAuthenticationRejectsEvents(t *testing.T) {
	spec := &agent.WebhookSpec{
		Type:   "authentication",
		Events: []string{"onStart"},
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL: stringPtr("https://example.com/tokenreviews"),
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.NotEmpty(t, errs, "a TokenReview hook is per request, not per run; events must be empty")
}

func TestValidateWebhookAuthenticationAcceptsAudiences(t *testing.T) {
	spec := &agent.WebhookSpec{
		Type:      "authentication",
		Audiences: []string{"onexai"},
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL: stringPtr("https://example.com/tokenreviews"),
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.Empty(t, errs)
}

func TestValidateWebhookAuthorizationRejectsAudiences(t *testing.T) {
	spec := &agent.WebhookSpec{
		Type:      "authorization",
		Audiences: []string{"onexai"},
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL: stringPtr("https://example.com/subjectaccessreviews"),
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.NotEmpty(t, errs, "audiences belong to TokenReview, not SubjectAccessReview")
}

func TestValidateWebhookRunStillRequiresEvents(t *testing.T) {
	spec := &agent.WebhookSpec{
		Type: "run",
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL: stringPtr("https://example.com/hook"),
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.NotEmpty(t, errs)
}

func TestValidateWebhookAuthRejectsTemplate(t *testing.T) {
	spec := &agent.WebhookSpec{
		Type:     "authorization",
		Template: `{"custom":true}`,
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL: stringPtr("https://example.com/subjectaccessreviews"),
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.NotEmpty(t, errs, "the access-review body is defined by Kubernetes, not by a template")
}

func TestValidateWebhookRejectsAnUnknownType(t *testing.T) {
	spec := &agent.WebhookSpec{
		Type: "audit",
		ClientConfig: &agent.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL: stringPtr("https://example.com/hook"),
			},
		},
	}
	errs := ValidateWebhook(spec, field.NewPath("spec"))
	assert.NotEmpty(t, errs)
}

func stringPtr(value string) *string { return &value }
