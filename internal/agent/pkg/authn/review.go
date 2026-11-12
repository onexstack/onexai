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

package authn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/clientconfig"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

const (
	// maxReviewResponseBytes bounds what a misbehaving webhook can make the
	// platform buffer. A TokenReview/SAR response is a few hundred bytes.
	maxReviewResponseBytes = 1 << 20
	// defaultReviewTimeout matches the Webhook resource's default.
	defaultReviewTimeout = 30 * time.Second
)

// SecretValueFunc resolves a Webhook's secretRef to the value sent with the
// call, and reports whether the reference is known. A reference that cannot be
// resolved is an error rather than an unauthenticated call: the receiver would
// otherwise have to trust the URL alone.
type SecretValueFunc func(ctx context.Context, ref string) (string, bool)

// ReviewOutcome is what the wire said, for the console's test action.
type ReviewOutcome struct {
	HTTPStatus int           `json:"httpStatus"`
	Latency    time.Duration `json:"-"`
	LatencyMs  int64         `json:"latencyMs"`
	Raw        string        `json:"raw"`
}

// ReviewCaller sends Kubernetes access-review objects to a Webhook.
//
// It is deliberately the only implementation of "call a webhook that speaks
// TokenReview / SubjectAccessReview": the Agent authentication path and the
// console's test button must not drift into two answers for one endpoint.
type ReviewCaller struct {
	secrets SecretValueFunc
}

// NewReviewCaller builds a caller over the platform's secret store.
func NewReviewCaller(secrets SecretValueFunc) *ReviewCaller {
	return &ReviewCaller{secrets: secrets}
}

// TokenReview posts a TokenReview to hook and returns its status.
//
// A non-2xx answer, an unreadable body or a timeout is an error: the caller
// treats every one of them as "this webhook did not authenticate the token".
func (c *ReviewCaller) TokenReview(
	ctx context.Context, hook *model.WebhookM, spec authenticationv1.TokenReviewSpec,
) (*authenticationv1.TokenReviewStatus, *ReviewOutcome, error) {
	review := authenticationv1.TokenReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"},
		Spec:     spec,
	}
	body, err := json.Marshal(review)
	if err != nil {
		return nil, nil, err
	}
	outcome, raw, err := c.post(ctx, hook, body)
	if err != nil {
		return nil, outcome, err
	}
	var answer authenticationv1.TokenReview
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, outcome, fmt.Errorf("token review response is not a TokenReview: %w", err)
	}
	return &answer.Status, outcome, nil
}

// SubjectAccessReview posts a SubjectAccessReview to hook and returns its
// status.
func (c *ReviewCaller) SubjectAccessReview(
	ctx context.Context, hook *model.WebhookM, review authorizationv1.SubjectAccessReview,
) (*authorizationv1.SubjectAccessReviewStatus, *ReviewOutcome, error) {
	review.TypeMeta = metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}
	body, err := json.Marshal(review)
	if err != nil {
		return nil, nil, err
	}
	outcome, raw, err := c.post(ctx, hook, body)
	if err != nil {
		return nil, outcome, err
	}
	var answer authorizationv1.SubjectAccessReview
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, outcome, fmt.Errorf("subject access review response is not a SubjectAccessReview: %w", err)
	}
	return &answer.Status, outcome, nil
}

// post sends one review and reads its answer. The outcome is returned even on
// error so the console can show what the endpoint actually said.
func (c *ReviewCaller) post(ctx context.Context, hook *model.WebhookM, body []byte) (*ReviewOutcome, []byte, error) {
	if hook == nil {
		return nil, nil, fmt.Errorf("webhook is nil")
	}
	endpoint, client, err := reviewEndpoint(hook)
	if err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "onexai-agent-apiserver")
	for name, value := range hook.Headers {
		req.Header.Set(name, value)
	}
	if hook.SecretRef != "" {
		if c.secrets == nil {
			return nil, nil, fmt.Errorf("webhook %q names a secret but the deployment has no secret store", hook.Name)
		}
		secret, ok := c.secrets(ctx, hook.SecretRef)
		if !ok {
			return nil, nil, fmt.Errorf("webhook %q names a secret that cannot be resolved", hook.Name)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		// Run hooks carry their token here; sending it as well lets one receiver
		// serve both kinds without a second credential field.
		req.Header.Set("X-Onexai-Token", secret)
	}

	start := time.Now()
	resp, err := client.Do(req)
	outcome := &ReviewOutcome{Latency: time.Since(start)}
	if err != nil {
		outcome.LatencyMs = outcome.Latency.Milliseconds()
		return outcome, nil, err
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxReviewResponseBytes))
	outcome.HTTPStatus = resp.StatusCode
	outcome.Raw = string(raw)
	outcome.LatencyMs = outcome.Latency.Milliseconds()
	if readErr != nil {
		return outcome, nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return outcome, raw, fmt.Errorf("webhook %q answered %s", hook.Name, resp.Status)
	}
	return outcome, raw, nil
}

// reviewEndpoint resolves the full route a review is posted to.
//
// The Webhook's URL is the complete route, not a base: a TokenReview receiver
// is usually a Kubernetes-compatible endpoint such as
// /apis/authentication.k8s.io/v1/tokenreviews, and appending a path here would
// make an existing receiver unreachable.
func reviewEndpoint(hook *model.WebhookM) (string, *http.Client, error) {
	if hook.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(hook.ClientConfigJSON), cfg); err != nil {
			return "", nil, fmt.Errorf("webhook %q has an unreadable clientConfig: %w", hook.Name, err)
		}
		// The top-level timeout is the operator-facing one; clientConfig's copy
		// is legacy. Applying it here also keys the client cache correctly.
		if hook.TimeoutSeconds > 0 {
			cfg.TimeoutSeconds = int32(hook.TimeoutSeconds)
		}
		resolved, err := clientconfig.Resolve(*cfg)
		if err != nil {
			return "", nil, fmt.Errorf("webhook %q: %w", hook.Name, err)
		}
		client, err := clientconfig.HTTPClient(*cfg)
		if err != nil {
			return "", nil, fmt.Errorf("webhook %q: %w", hook.Name, err)
		}
		return resolved.URL, client, nil
	}
	if hook.URL == "" {
		return "", nil, fmt.Errorf("webhook %q has no endpoint", hook.Name)
	}
	timeout := time.Duration(hook.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultReviewTimeout
	}
	return hook.URL, &http.Client{Timeout: timeout}, nil
}
