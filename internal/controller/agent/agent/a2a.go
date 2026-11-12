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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onexstack/onexai/internal/agent/pkg/a2aclient"
	"github.com/onexstack/onexai/internal/controller/support"
	"github.com/onexstack/onexai/internal/pkg/netguard"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

const (
	// a2aRefreshAnnotation asks the controller to fetch the card now. The API
	// sets it; the controller records the value it acted on so a retry does not
	// refetch forever.
	a2aRefreshAnnotation = "agent.onexai.io/a2a-refresh-at"

	a2aSyncInterval = 10 * time.Minute
	a2aRetryDelay   = time.Minute
	a2aFetchTimeout = 10 * time.Second
	a2aMaxCardBytes = 256 << 10
	a2aHistoryLimit = 10
)

// a2aSyncResult is one card reconciliation.
type a2aSyncResult struct {
	Status       *agentv1alpha1.AgentA2AStatus
	Conditions   []metav1.Condition
	Ready        bool
	Message      string
	RequeueAfter time.Duration
}

// reconcileExternal handles an agent whose behavior lives in a remote
// AgentCard. It intentionally skips the native prompt/capability resolver: an
// external agent has no platform-defined behavior to resolve.
func (r *Reconciler) reconcileExternal(ctx context.Context, agent *agentv1alpha1.Agent) (ctrl.Result, error) {
	result := r.syncA2A(ctx, agent)

	status := agent.Status.DeepCopy()
	status.ObservedGeneration = agent.Generation
	status.Resolved = agentv1alpha1.AgentResolved{}
	status.ResolvedHash = ""
	status.BindingContributions = nil
	status.BindingContributionsTotal = 0
	status.BindingContributionsTruncated = false
	status.AggregatedPrompt = ""
	status.AggregatedInputHash = ""
	status.AggregatedPromptHash = ""
	status.AggregatedPromptOverridden = false
	status.AggregatedPromptStale = false
	status.ObservedRegenerateToken = ""
	status.A2A = result.Status
	status.Conditions = nil
	for _, condition := range result.Conditions {
		if condition.Type == "" {
			continue
		}
		apimeta.SetStatusCondition(&status.Conditions, condition)
	}
	support.SetReady(r.Recorder, agent, &status.Conditions, agent.Generation, result.Ready,
		reasonForReady(result.Ready), result.Message)

	if apiequality.Semantic.DeepEqual(&agent.Status, status) {
		return ctrl.Result{RequeueAfter: result.RequeueAfter}, nil
	}
	before := agent.DeepCopy()
	agent.Status = *status
	if err := r.Status().Patch(ctx, agent, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{RequeueAfter: result.RequeueAfter}, nil
}

// syncA2A fetches and normalizes the card. It never deletes the last good card:
// a transient outage must leave the agent usable with its previous contract.
func (r *Reconciler) syncA2A(ctx context.Context, agent *agentv1alpha1.Agent) a2aSyncResult {
	execution := agent.Spec.Execution
	if execution == nil || execution.Mode != agentv1alpha1.ExecutionModeA2A || execution.A2A == nil {
		return a2aSyncResult{
			Status: agent.Status.A2A,
			Conditions: []metav1.Condition{cond("CardFetched", false,
				"ExecutionNotConfigured", "spec.execution.a2a is required", agent.Generation)},
			Message:      "A2A execution is not configured",
			RequeueAfter: a2aRetryDelay,
		}
	}
	a := execution.A2A

	endpoint := a.Endpoint
	profileAuthHeaders, _, profileMessage, profileCondition := r.profileAuth(ctx, agent, a)
	if profileCondition != nil {
		return a2aSyncResult{
			Status:       agent.Status.A2A,
			Conditions:   []metav1.Condition{*profileCondition},
			Message:      profileMessage,
			RequeueAfter: a2aRetryDelay,
		}
	}
	if a.RuntimeRef != nil {
		profile := &agentv1alpha1.RuntimeProfile{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: a.RuntimeRef.Name}, profile); err != nil {
			return degraded(agent, "RuntimeNotReady", "the runtime profile could not be read: "+err.Error())
		}
		if !profile.Spec.Enabled {
			return degraded(agent, "RuntimeNotReady", "the runtime profile is disabled")
		}
		endpoint = profile.Status.ResolvedEndpoint
		if endpoint == "" {
			endpoint = profile.Spec.Endpoint
		}
	}

	headers := profileAuthHeaders
	if headers == nil {
		headers = map[string]string{}
	}
	authHeaders, authReady, authMessage := r.credentialHeaders(ctx, agent, a.Auth)
	for k, v := range authHeaders {
		headers[k] = v
	}
	if !authReady {
		return a2aSyncResult{
			Status:       agent.Status.A2A,
			Conditions:   []metav1.Condition{cond("AuthResolved", false, "CredentialIncomplete", authMessage, agent.Generation)},
			Message:      authMessage,
			RequeueAfter: a2aRetryDelay,
		}
	}

	var card *a2aclient.Card
	var err error
	if a.Source == agentv1alpha1.A2ASourceJSON || a.Source == agentv1alpha1.A2ASourceManual {
		if a.Card == nil || len(a.Card.Raw) == 0 {
			return degraded(agent, "CardMissing", "a json/manual source requires an AgentCard")
		}
		card, err = a2aclient.ParseCard(a.Card.Raw)
		if card != nil && a.Endpoint != "" {
			card.ResolveRelative(a.Endpoint)
		}
	} else {
		candidates := a2aclient.CandidateCardURLs(endpoint, a.CardURL)
		if len(candidates) == 0 {
			return degraded(agent, "NoEndpoint", "a2a execution needs cardUrl or endpoint")
		}
		fetcher := netguard.NewFetcher(netguard.Options{
			AllowPrivateHosts: true,
			MaxBytes:          a2aMaxCardBytes,
			Timeout:           a2aFetchTimeout,
			MaxRedirects:      3,
			Retries:           -1,
		})
		httpClient := a2aclient.TimeoutClient(fetcher.Client(), a2aFetchTimeout)
		httpClient = a2aclient.ClientWithHeaders(httpClient, headers)
		if a.Auth != nil && a.Auth.Type == "aksk" {
			accessKey, secretKey, ready, message := r.akskCredentials(ctx, agent, a.Auth)
			if !ready {
				return a2aSyncResult{
					Status:       agent.Status.A2A,
					Conditions:   []metav1.Condition{cond("AuthResolved", false, "CredentialIncomplete", message, agent.Generation)},
					Message:      message,
					RequeueAfter: a2aRetryDelay,
				}
			}
			transport := httpClient.Transport
			if transport == nil {
				transport = http.DefaultTransport
			}
			httpClient.Transport = a2aclient.NewAKSKTransport(transport, accessKey, secretKey)
		}
		card, err = a2aclient.FetchCard(ctx, httpClient, candidates, nil)
	}
	if err != nil {
		return degraded(agent, "CardFetchFailed", err.Error())
	}

	now := metav1.Now()
	status := &agentv1alpha1.AgentA2AStatus{
		Phase:            agentv1alpha1.AgentPhaseReady,
		ProtocolVersion:  card.ProtocolVersion,
		Transport:        card.Transport(),
		ResolvedEndpoint: card.Endpoint(),
		CardVersion:      card.Version,
		CardDigest:       card.Digest,
		CardFetchedAt:    &now,
		Provider:         card.Provider,
		AuthReady:        true,
		Capabilities: agentv1alpha1.A2ACapabilities{
			Streaming:              card.Capabilities.Streaming,
			PushNotifications:      card.Capabilities.PushNotifications,
			ExtendedAgentCard:      card.Capabilities.ExtendedAgentCard,
			StateTransitionHistory: card.Capabilities.StateTransitionHistory,
		},
		InputModes:  card.InputModes,
		OutputModes: card.OutputModes,
	}
	if status.ResolvedEndpoint == "" {
		status.ResolvedEndpoint = endpoint
	}
	for _, skill := range card.Skills {
		status.Skills = append(status.Skills, agentv1alpha1.A2ASkill{
			ID:          skill.ID,
			Name:        skill.Name,
			Description: skill.Description,
			Tags:        skill.Tags,
			Examples:    skill.Examples,
			InputModes:  skill.InputModes,
			OutputModes: skill.OutputModes,
		})
	}
	for _, ext := range card.Extensions {
		status.Extensions = append(status.Extensions, agentv1alpha1.A2AExtension{
			URI: ext.URI, Description: ext.Description, Required: ext.Required,
		})
	}
	if len(card.SecuritySchemes) > 0 {
		status.SecuritySchemes = &runtime.RawExtension{Raw: card.SecuritySchemes}
	}
	// The card itself is kept only when it fits the status budget. The detail
	// page fetches larger cards live through the card endpoint.
	if len(card.Raw) <= a2aMaxCardBytes {
		status.Card = &runtime.RawExtension{Raw: card.Raw}
	}
	status.CardHistory = cardHistory(agent.Status.A2A, card, now)
	status.ObservedRefreshToken = agent.Annotations[a2aRefreshAnnotation]

	transport := card.Transport()
	compatible := transport == a2aclient.TransportJSONRPC || transport == a2aclient.TransportHTTPJSON
	return a2aSyncResult{
		Status: status,
		Conditions: []metav1.Condition{
			cond("CardFetched", true, "Fetched", "the AgentCard was fetched and parsed", agent.Generation),
			cond("AuthResolved", true, "Accepted", "the credential is complete", agent.Generation),
			cond("RuntimeResolved", true, "Accepted", "the endpoint is resolved", agent.Generation),
			cond("Compatible", compatible, compatibleReason(compatible), compatibleMessage(transport), agent.Generation),
		},
		Ready:        compatible,
		Message:      compatibleMessage(transport),
		RequeueAfter: a2aSyncInterval,
	}
}

// degraded keeps the last good card and reports why the refresh failed.
func degraded(agent *agentv1alpha1.Agent, reason, message string) a2aSyncResult {
	status := agent.Status.A2A
	if status == nil {
		status = &agentv1alpha1.AgentA2AStatus{}
	}
	status.Phase = agentv1alpha1.AgentPhaseDegraded
	status.LastSyncError = message
	if status.CardVersion == "" {
		status.Phase = agentv1alpha1.AgentPhaseError
	}
	return a2aSyncResult{
		Status:       status,
		Conditions:   []metav1.Condition{cond("CardFetched", false, reason, message, agent.Generation)},
		Message:      message,
		RequeueAfter: a2aRetryDelay,
	}
}

// profileAuth reads a RuntimeProfile's bearer token when the agent delegates
// credentials to it. It returns nil headers when the profile has no auth.
func (r *Reconciler) profileAuth(
	ctx context.Context, agent *agentv1alpha1.Agent, a *agentv1alpha1.A2AExecution,
) (map[string]string, bool, string, *metav1.Condition) {
	if a.Auth == nil || a.Auth.Type != "runtime" {
		return nil, true, "", nil
	}
	if a.RuntimeRef == nil {
		c := cond("AuthResolved", false, "RuntimeNotConfigured",
			"auth.type=runtime needs a runtimeRef", agent.Generation)
		return nil, false, "auth.type=runtime needs a runtimeRef", &c
	}
	profile := &agentv1alpha1.RuntimeProfile{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: a.RuntimeRef.Name}, profile); err != nil {
		c := cond("AuthResolved", false, "RuntimeNotReady", err.Error(), agent.Generation)
		return nil, false, err.Error(), &c
	}
	if profile.Spec.Auth == nil || profile.Spec.Auth.BearerTokenRef == nil {
		return nil, true, "", nil
	}
	secret := &corev1.Secret{}
	ref := profile.Spec.Auth.BearerTokenRef
	if err := r.Get(ctx, client.ObjectKey{Namespace: profile.Namespace, Name: ref.Name}, secret); err != nil {
		c := cond("AuthResolved", false, "CredentialMissing", err.Error(), agent.Generation)
		return nil, false, err.Error(), &c
	}
	token := string(secret.Data[ref.Key])
	if token == "" {
		c := cond("AuthResolved", false, "CredentialIncomplete",
			"the runtime profile bearer key is empty", agent.Generation)
		return nil, false, "the runtime profile bearer key is empty", &c
	}
	return map[string]string{"Authorization": "Bearer " + token}, true, "", nil
}

// credentialHeaders reads the agent's own credential Secret and turns it into
// request headers. A missing key is a configuration error, not an empty header:
// sending an unauthenticated request would turn "the secret is incomplete" into
// "the peer said 401".
func (r *Reconciler) credentialHeaders(
	ctx context.Context, agent *agentv1alpha1.Agent, auth *agentv1alpha1.A2AAuth,
) (map[string]string, bool, string) {
	if auth == nil || auth.Type == "" || auth.Type == "none" {
		return nil, true, ""
	}
	if auth.Type == "aksk" {
		// Signed per request by akskTransport; nothing static to set here.
		return nil, true, ""
	}
	secretName := agent.Name + "-a2a-credentials"
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: secretName}, secret); err != nil {
		return nil, false, "the credential Secret could not be read: " + err.Error()
	}
	headers := map[string]string{}
	switch auth.Type {
	case "apiKey":
		value := string(secret.Data["apiKey"])
		if value == "" {
			return nil, false, "the apiKey credential is empty"
		}
		name := auth.APIKeyHeader
		if name == "" {
			name = "X-API-KEY"
		}
		headers[name] = value
	case "bearer":
		value := string(secret.Data["token"])
		if value == "" {
			return nil, false, "the bearer token is empty"
		}
		headers["Authorization"] = "Bearer " + value
	case "headers":
		raw := secret.Data["headers"]
		if len(raw) == 0 {
			return nil, false, "the custom headers credential is empty"
		}
		if err := json.Unmarshal(raw, &headers); err != nil {
			return nil, false, "the custom headers credential is not valid JSON"
		}
	case "oauth2":
		clientID := string(secret.Data["clientID"])
		if clientID == "" {
			clientID = auth.ClientID
		}
		clientSecret := string(secret.Data["clientSecret"])
		if auth.TokenURL == "" || clientID == "" || clientSecret == "" {
			return nil, false, "OAuth2 needs tokenUrl, clientID and clientSecret"
		}
		cfg := clientcredentials.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			TokenURL:     auth.TokenURL,
			Scopes:       auth.Scopes,
		}
		if auth.Audience != "" {
			cfg.EndpointParams = url.Values{"audience": {auth.Audience}}
		}
		switch auth.ClientAuth {
		case "post":
			cfg.AuthStyle = oauth2.AuthStyleInParams
		case "basic":
			cfg.AuthStyle = oauth2.AuthStyleInHeader
		}
		fetcher := netguard.NewFetcher(netguard.Options{
			AllowPrivateHosts: true, Timeout: a2aFetchTimeout, MaxRedirects: 3, Retries: -1,
		})
		tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, fetcher.Client())
		token, err := cfg.Token(tokenCtx)
		if err != nil {
			return nil, false, "OAuth2 token request failed: " + err.Error()
		}
		headers["Authorization"] = "Bearer " + token.AccessToken
	default:
		return nil, false, fmt.Sprintf("unknown auth type %q", auth.Type)
	}
	return headers, true, ""
}

func cardHistory(
	old *agentv1alpha1.AgentA2AStatus, card *a2aclient.Card, now metav1.Time,
) []agentv1alpha1.A2ACardSnapshot {
	var history []agentv1alpha1.A2ACardSnapshot
	if old != nil {
		history = append(history, old.CardHistory...)
	}
	if old != nil && old.CardDigest == card.Digest {
		return history
	}
	snapshot := agentv1alpha1.A2ACardSnapshot{
		Version:        card.Version,
		Digest:         card.Digest,
		FetchedAt:      &now,
		SkillCount:     int32(len(card.Skills)),
		InterfaceCount: int32(len(card.Interfaces)),
	}
	history = append(history, snapshot)
	if len(history) > a2aHistoryLimit {
		history = history[len(history)-a2aHistoryLimit:]
	}
	return history
}

func cond(typ string, ok bool, reason, message string, generation int64) metav1.Condition {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	}
}

func reasonForReady(ready bool) string {
	if ready {
		return "Synced"
	}
	return "SyncFailed"
}

func compatibleReason(ok bool) string {
	if ok {
		return "Supported"
	}
	return "UnsupportedTransport"
}

func compatibleMessage(transport string) string {
	switch transport {
	case a2aclient.TransportJSONRPC, a2aclient.TransportHTTPJSON:
		return "the card exposes a supported transport"
	case a2aclient.TransportGRPC:
		return "gRPC transports are not supported yet"
	case "":
		return "the card exposes no supported interface"
	default:
		return "unsupported transport: " + transport
	}
}

// akskCredentials reads the service credential used to sign card and invoke
// requests to another onexai A2A surface.
func (r *Reconciler) akskCredentials(
	ctx context.Context, agent *agentv1alpha1.Agent, _ *agentv1alpha1.A2AAuth,
) (string, string, bool, string) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: agent.Name + "-a2a-credentials"}, secret); err != nil {
		return "", "", false, "the AK/SK credential Secret could not be read: " + err.Error()
	}
	accessKey := string(secret.Data["accessKey"])
	secretKey := string(secret.Data["secretKey"])
	if accessKey == "" || secretKey == "" {
		return "", "", false, "the AK/SK credential is incomplete"
	}
	return accessKey, secretKey, true, ""
}
