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

// Package webhook implements authz.Authorizer by delegating decisions to an
// external HTTP endpoint implementing the Kubernetes
// authorization.k8s.io/v1 SubjectAccessReview contract.
//
// Using the Kubernetes type rather than a bespoke one is the point: any
// policy center that already speaks SubjectAccessReview — OPA, Casbin, a
// kube-apiserver, a vendor PDP — can be pointed at without an adapter. The
// optional HMAC signature (X-Onex-Signature) is a platform extension for
// receivers that want proof the body came from this service; a Kubernetes
// policy server simply ignores the header.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// SignatureHeader carries the HMAC of the request body, so a receiver can tell a
// request this service sent from one anybody could POST to the same URL.
const SignatureHeader = "X-Onex-Signature"

// Signature returns the value of SignatureHeader for body under secret:
// "sha256=<hex>". It is exported so a receiver implements the same computation
// instead of re-deriving it from the docs.
func Signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Authorizer calls a single external HTTP endpoint implementing the
// SubjectAccessReview contract.
type Authorizer struct {
	name        string
	endpoint    string
	httpClient  *http.Client
	failureMode FailureMode
	// secret, when non-empty, signs every request body (see Signature). Empty
	// sends unsigned requests — the behaviour every deployment had before the
	// option existed, kept so an unconfigured endpoint still works.
	secret string
	// headers are sent with every request. They carry the routing labels a
	// Webhook resource declares.
	headers map[string]string
	// bearerToken, when non-empty, is the credential a Webhook resource names
	// with secretRef: Authorization: Bearer plus X-Onexai-Token, matching the
	// platform's webhook convention.
	bearerToken string
}

// NewAuthorizer builds a webhook-backed Authorizer. name is used for
// logging/metrics (Authorizer.Name()) so multiple webhook endpoints Union'd
// together remain distinguishable — pass the configured
// options.WebhookEndpoint.Name.
func NewAuthorizer(name, endpoint string, timeout time.Duration, mode FailureMode) *Authorizer {
	if mode == "" {
		mode = FailureModeNoOpinion
	}
	return &Authorizer{
		name:        name,
		endpoint:    endpoint,
		httpClient:  &http.Client{Timeout: timeout},
		failureMode: mode,
	}
}

// WithSecret makes every request carry SignatureHeader computed from the
// request body. It returns the receiver so the call can be chained at the
// construction site.
func (a *Authorizer) WithSecret(secret string) *Authorizer {
	a.secret = secret
	return a
}

func (a *Authorizer) Name() string { return "webhook:" + a.name }

// WithHTTPClient replaces the client built from timeout. It is how a Webhook
// resource's ClientConfig (Service reference, CA bundle) reaches the call.
func (a *Authorizer) WithHTTPClient(client *http.Client) *Authorizer {
	if client != nil {
		a.httpClient = client
	}
	return a
}

// WithHeaders adds headers sent with every request.
func (a *Authorizer) WithHeaders(headers map[string]string) *Authorizer {
	if len(headers) > 0 {
		a.headers = headers
	}
	return a
}

// WithBearer makes every request carry the Webhook resource's secretRef as a
// bearer credential. It is separate from WithSecret, which is the legacy
// HMAC-signed configuration path.
func (a *Authorizer) WithBearer(token string) *Authorizer {
	a.bearerToken = token
	return a
}

func (a *Authorizer) Authorize(ctx context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
	reviewReq := toSubjectAccessReview(req)

	body, err := json.Marshal(reviewReq)
	if err != nil {
		return a.onFailure(fmt.Errorf("webhook %s: marshal request: %w", a.name, err))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return a.onFailure(fmt.Errorf("webhook %s: build request: %w", a.name, err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "onexai-agent-apiserver")
	for name, value := range a.headers {
		httpReq.Header.Set(name, value)
	}
	if a.bearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.bearerToken)
		httpReq.Header.Set("X-Onexai-Token", a.bearerToken)
	}
	if a.secret != "" {
		httpReq.Header.Set(SignatureHeader, Signature(a.secret, body))
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return a.onFailure(fmt.Errorf("webhook %s: request failed: %w", a.name, err))
	}
	defer resp.Body.Close()

	// A Kubernetes API server answers a create with 201; a policy server may
	// answer 200. Both are success; anything outside 2xx is a failed call.
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return a.onFailure(fmt.Errorf("webhook %s: unexpected status %d", a.name, resp.StatusCode))
	}

	var reviewResp authorizationv1.SubjectAccessReview
	if err := json.NewDecoder(resp.Body).Decode(&reviewResp); err != nil {
		return a.onFailure(fmt.Errorf("webhook %s: decode response: %w", a.name, err))
	}
	if msg := reviewResp.Status.EvaluationError; msg != "" {
		return a.onFailure(fmt.Errorf("webhook %s: evaluation error: %s", a.name, msg))
	}

	switch {
	case reviewResp.Status.Allowed:
		return authz.DecisionAllow, reviewResp.Status.Reason, nil
	case reviewResp.Status.Denied:
		return authz.DecisionDeny, reviewResp.Status.Reason, nil
	default:
		// Neither allowed nor denied: the webhook has no opinion. This is not a
		// grant; the Union or the caller's fail-closed default decides.
		return authz.DecisionNoOpinion, reviewResp.Status.Reason, nil
	}
}

func (a *Authorizer) onFailure(err error) (authz.Decision, string, error) {
	return a.decisionForFailureMode(), "", err
}

func (a *Authorizer) decisionForFailureMode() authz.Decision {
	if a.failureMode == FailureModeDeny {
		return authz.DecisionDeny
	}
	return authz.DecisionNoOpinion
}

// toSubjectAccessReview is the one mapping from the platform's authorization
// request to the Kubernetes contract. It is exported to tests only by being in
// the same package; callers outside this package should use Authorize.
func toSubjectAccessReview(req authz.AuthorizationRequest) *authorizationv1.SubjectAccessReview {
	kind, name := req.EffectiveResource()

	out := &authorizationv1.SubjectAccessReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: authorizationv1.SchemeGroupVersion.String(),
			Kind:       "SubjectAccessReview",
		},
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   req.Subject.PrimaryID(),
			Groups: groupList(req.Subject),
			Extra:  map[string]authorizationv1.ExtraValue{},
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     sarVerb(req.Verb),
				Group:    "onexai.io",
				Version:  "v1",
				Resource: string(kind),
				Name:     name,
			},
		},
	}

	if req.Resource.Attributes != nil {
		out.Spec.ResourceAttributes.Namespace = req.Resource.Attributes["namespace"]
		out.Spec.ResourceAttributes.Subresource = req.Resource.Attributes["subresource"]
		for key, value := range req.Resource.Attributes {
			switch key {
			case "namespace", "subresource":
				continue
			}
			addExtra(out.Spec.Extra, "onexai.io/attr/"+key, value)
		}
	}

	addExtra(out.Spec.Extra, "onexai.io/subject-kind", string(req.Subject.Kind))
	addExtra(out.Spec.Extra, "onexai.io/agent-name", req.AgentName)
	addExtra(out.Spec.Extra, "onexai.io/agent-names", strings.Join(req.EffectiveAgentNames(), ","))
	addExtra(out.Spec.Extra, "onexai.io/tool-name", req.ToolName)
	addExtra(out.Spec.Extra, "onexai.io/mcp-name", req.MCPName)

	return out
}

// sarVerb maps the platform's two-verb model onto the Kubernetes vocabulary.
// A read is a "get"; a write is an "update". A policy server that needs a more
// specific verb can look at onexai.io/tool-name in Extra.
func sarVerb(verb authz.Verb) string {
	if verb == authz.VerbWrite {
		return "update"
	}
	return "get"
}

// groupList exposes the group codes the platform already carries on a user
// subject. Kubernetes carries groups as a first-class field, so mapping them
// here lets a Kubernetes RBAC or webhook authorizer use the same rule it would
// use for any other user.
func groupList(subj authz.Subject) []string {
	if subj.Kind != authz.SubjectKindUser {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, key := range []string{"group_codes", "oncall_group_codes"} {
		for _, code := range strings.Split(subj.Extra[key], ",") {
			code = strings.TrimSpace(code)
			if code == "" {
				continue
			}
			if _, ok := seen[code]; ok {
				continue
			}
			seen[code] = struct{}{}
			out = append(out, code)
		}
	}
	return out
}

func addExtra(extra map[string]authorizationv1.ExtraValue, key, value string) {
	if key == "" || value == "" {
		return
	}
	extra[key] = authorizationv1.ExtraValue{value}
}
