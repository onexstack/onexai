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
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	metav1apply "k8s.io/client-go/applyconfigurations/meta/v1"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	applyconfigurations "github.com/onexstack/onexai/pkg/generated/applyconfigurations/agent/v1alpha1"
)

// RuntimeFieldManager is the server-side-apply manager the runtime publishes its
// conditions under. The controller-manager writes its own conditions under a
// different manager, and apply merges the two by condition type instead of one
// overwriting the other.
const RuntimeFieldManager = "onexai-agent-runtime"

// ProbeTool is one tool a probed MCP server publishes.
type ProbeTool struct {
	Name        string
	Description string
}

// MCPServerProber performs one MCP handshake for the named server.
//
// It is an interface rather than a direct dependency on the MCP client because
// this package must not know how a server is dialed — and because the probe is
// the one place the handshake's failure is wanted, while the run path swallows
// it.
type MCPServerProber interface {
	Probe(ctx context.Context, name string) ([]ProbeTool, error)
}

// SkillProber resolves the named skill's source.
type SkillProber interface {
	// Probe returns how many skills the source contains.
	Probe(ctx context.Context, name string) (int, error)
}

// KnowledgeBaseProber runs one minimal retrieval against the named knowledge
// base, returning how many passages the platform answered with.
type KnowledgeBaseProber interface {
	Probe(ctx context.Context, name string) (int, error)
}

// Probers are the checks only the runtime can perform.
//
// Each is optional: a deployment without the subsystem has nothing to say about
// it, and its resources keep whatever the controller last reported.
type Probers struct {
	MCP            MCPServerProber
	Skills         SkillProber
	KnowledgeBases KnowledgeBaseProber
}

// HealthReporter probes the resources whose health only the runtime can see and
// publishes the result into their status.
//
// It exists because the controller-manager deliberately does not hold model
// credentials or HTTP clients: duplicating the provider client (and its SSRF
// policy) into the control plane would be a second implementation. The runtime
// already talks to these systems, so the runtime is what reports on them.
type HealthReporter struct {
	client        *declclient.Client
	interval      time.Duration
	http          *http.Client
	probers       Probers
	encryptionKey string
}

// NewHealthReporter builds a reporter that probes every interval. encryptionKey
// opens model credentials sealed at rest; empty leaves legacy plaintext rows
// usable and reports sealed ones as unreadable instead of probing with them.
func NewHealthReporter(client *declclient.Client, interval time.Duration, probers Probers, encryptionKey string) *HealthReporter {
	if interval <= 0 {
		interval = time.Minute
	}
	return &HealthReporter{
		client:        client,
		interval:      interval,
		probers:       probers,
		encryptionKey: encryptionKey,
		http: &http.Client{
			// A model endpoint that cannot answer in ten seconds is not usable for
			// a run, so the probe does not wait longer than a run would.
			Timeout: 10 * time.Second,
		},
	}
}

// Run probes until the context is cancelled. The first pass runs immediately so
// a restart republishes the truth instead of waiting one interval.
func (h *HealthReporter) Run(ctx context.Context) {
	if h == nil || h.client == nil {
		return
	}

	h.probeAll(ctx)

	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.probeAll(ctx)
		}
	}
}

// probeAll runs every prober. Each one is independent: a provider that is down
// must not stop the others from being reported.
func (h *HealthReporter) probeAll(ctx context.Context) {
	h.probeModels(ctx)
	h.probeWebhooks(ctx)
	h.probeMCPServers(ctx)
	h.probeSkills(ctx)
	h.probeKnowledgeBases(ctx)
}

// probeWebhooks checks that each hook's endpoint answers at all.
//
// The probe is unauthenticated on purpose: the token lives in the runtime's
// secret store, which this process does not read, and a 401 already proves the
// endpoint is reachable. Whether the token is accepted stays the dispatcher's
// finding, reported through the hook's own failure count.
func (h *HealthReporter) probeWebhooks(ctx context.Context) {
	hooks, err := h.client.Webhooks().List(labels.Everything())
	if err != nil {
		slog.WarnContext(ctx, "health: could not list webhooks", "error", err)
		return
	}

	for _, hook := range hooks {
		if !hook.Spec.Enabled {
			continue
		}
		if err := h.probeWebhook(ctx, hook); err != nil {
			slog.WarnContext(ctx, "health: could not publish webhook health",
				"webhook", hook.Namespace+"/"+hook.Name, "error", err)
		}
	}
}

func (h *HealthReporter) probeWebhook(ctx context.Context, hook *agentv1alpha1.Webhook) error {
	reachable, message := h.callEndpoint(ctx, hook.Spec.Endpoint)

	conditions := []metav1.Condition{
		condition(hook.Status.Conditions, hook.Generation, agentv1alpha1.ConditionEndpointHealthy,
			reachable, reasonFor(reachable, "EndpointAnswered", "EndpointUnreachable"), message),
	}

	apply := applyconfigurations.Webhook(hook.Name, hook.Namespace).
		WithStatus(applyconfigurations.WebhookStatus().WithConditions(applyConditions(conditions)...))

	_, err := h.client.Clientset.AgentV1alpha1().Webhooks(hook.Namespace).ApplyStatus(
		ctx, apply, metav1.ApplyOptions{FieldManager: RuntimeFieldManager},
	)
	return err
}

// callEndpoint asks the URL for an answer of any kind.
func (h *HealthReporter) callEndpoint(ctx context.Context, endpoint string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Sprintf("invalid endpoint: %v", err)
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return false, fmt.Sprintf("request failed: %v", err)
	}
	defer resp.Body.Close()
	return true, fmt.Sprintf("the endpoint answered %d", resp.StatusCode)
}

// condition builds a condition, keeping the previous transition time when the
// status did not change so a steady probe does not rewrite the object.
//
// It is one helper for every resource: the runtime publishes its findings under
// a single field manager, and five probers that each formatted a condition their
// own way would be five chances to disagree with the API conventions.
func condition(existing []metav1.Condition, generation int64, conditionType string, ok bool, reason, message string) metav1.Condition {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}

	transition := metav1.Now()
	for _, current := range existing {
		if current.Type == conditionType && current.Status == status {
			transition = current.LastTransitionTime
			break
		}
	}

	return metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: transition,
		ObservedGeneration: generation,
	}
}

// messageOr is a probe's condition message: the failure in its own words when it
// failed, the given summary otherwise. A message an operator can act on is the
// point of reporting the probe at all.
func messageOr(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

// probeMCPServers performs the handshake nothing else in the deployment can:
// the controller has neither the MCP client nor the network path to the server.
func (h *HealthReporter) probeMCPServers(ctx context.Context) {
	if h.probers.MCP == nil {
		return
	}
	servers, err := h.client.MCPServers().List(labels.Everything())
	if err != nil {
		slog.WarnContext(ctx, "health: could not list mcp servers", "error", err)
		return
	}

	for _, server := range servers {
		if !server.Spec.Enabled {
			continue
		}
		if err := h.probeMCPServer(ctx, server); err != nil {
			slog.WarnContext(ctx, "health: could not publish mcp server health",
				"mcpserver", server.Namespace+"/"+server.Name, "error", err)
		}
	}
}

func (h *HealthReporter) probeMCPServer(ctx context.Context, server *agentv1alpha1.MCPServer) error {
	tools, probeErr := h.probers.MCP.Probe(ctx, server.Name)

	apply := applyconfigurations.MCPServer(server.Name, server.Namespace).
		WithStatus(mcpServerStatus(server, tools, probeErr))

	_, err := h.client.Clientset.AgentV1alpha1().MCPServers(server.Namespace).ApplyStatus(
		ctx, apply, metav1.ApplyOptions{FieldManager: RuntimeFieldManager},
	)
	return err
}

// mcpServerStatus decides what a probe means for one server's status.
//
// status.tools is what the last successful discovery found (as its field comment
// says), so a failed probe reports the failure but keeps the list a run would
// still be offered — losing it would make a transient network fault look like a
// server that had never published anything.
func mcpServerStatus(server *agentv1alpha1.MCPServer, tools []ProbeTool, probeErr error) *applyconfigurations.MCPServerStatusApplyConfiguration {
	discovered := server.Status.Tools
	lastDiscover := server.Status.LastDiscoverTime
	if probeErr == nil {
		discovered = make([]agentv1alpha1.MCPTool, 0, len(tools))
		for _, tool := range tools {
			discovered = append(discovered, agentv1alpha1.MCPTool{
				Name:        tool.Name,
				Description: tool.Description,
			})
		}
		now := metav1.Now()
		lastDiscover = &now
	}

	conditions := []metav1.Condition{
		condition(server.Status.Conditions, server.Generation, agentv1alpha1.ConditionHandshakeReady,
			probeErr == nil,
			reasonFor(probeErr == nil, "HandshakeSucceeded", "HandshakeFailed"),
			messageOr(probeErr, fmt.Sprintf("the server completed the handshake and published %d tools", len(tools)))),
	}

	status := applyconfigurations.MCPServerStatus().
		WithConditions(applyConditions(conditions)...).
		WithTools(applyMCPTools(discovered)...)
	if lastDiscover != nil {
		status = status.WithLastDiscoverTime(*lastDiscover)
	}
	return status
}

// probeSkills resolves each skill's source. The controller cannot: the provider
// credentials and the archive cache live in the runtime.
func (h *HealthReporter) probeSkills(ctx context.Context) {
	if h.probers.Skills == nil {
		return
	}
	skills, err := h.client.Skills().List(labels.Everything())
	if err != nil {
		slog.WarnContext(ctx, "health: could not list skills", "error", err)
		return
	}

	for _, s := range skills {
		if !s.Spec.Enabled {
			continue
		}
		if err := h.probeSkill(ctx, s); err != nil {
			slog.WarnContext(ctx, "health: could not publish skill health",
				"skill", s.Namespace+"/"+s.Name, "error", err)
		}
	}
}

func (h *HealthReporter) probeSkill(ctx context.Context, s *agentv1alpha1.Skill) error {
	count, probeErr := h.probers.Skills.Probe(ctx, s.Name)

	// LastSyncTime is when the source was last fetched successfully, so a failed
	// probe leaves it alone.
	lastSync := s.Status.LastSyncTime
	if probeErr == nil {
		now := metav1.Now()
		lastSync = &now
	}

	// One condition, not two: a source that answered has also been fetched, so a
	// separate Synced would only repeat this one's status.
	conditions := []metav1.Condition{
		condition(s.Status.Conditions, s.Generation, agentv1alpha1.ConditionSourceReachable,
			probeErr == nil,
			reasonFor(probeErr == nil, "SourceAnswered", "SourceUnanswered"),
			messageOr(probeErr, fmt.Sprintf("the %s source answered with %d skills", s.Spec.Provider, count))),
	}

	status := applyconfigurations.SkillStatus().WithConditions(applyConditions(conditions)...)
	if lastSync != nil {
		status = status.WithLastSyncTime(*lastSync)
	}
	apply := applyconfigurations.Skill(s.Name, s.Namespace).WithStatus(status)

	_, err := h.client.Clientset.AgentV1alpha1().Skills(s.Namespace).ApplyStatus(
		ctx, apply, metav1.ApplyOptions{FieldManager: RuntimeFieldManager},
	)
	return err
}

// probeKnowledgeBases asks each retrieval platform the cheapest question that
// proves both reachability and whether the credential is accepted. The platform
// configuration exists only in the runtime, which is why the controller cannot
// answer it.
func (h *HealthReporter) probeKnowledgeBases(ctx context.Context) {
	if h.probers.KnowledgeBases == nil {
		return
	}
	kbs, err := h.client.KnowledgeBases().List(labels.Everything())
	if err != nil {
		slog.WarnContext(ctx, "health: could not list knowledge bases", "error", err)
		return
	}

	for _, kb := range kbs {
		if !kb.Spec.Enabled {
			continue
		}
		if err := h.probeKnowledgeBase(ctx, kb); err != nil {
			slog.WarnContext(ctx, "health: could not publish knowledge base health",
				"knowledgebase", kb.Namespace+"/"+kb.Name, "error", err)
		}
	}
}

func (h *HealthReporter) probeKnowledgeBase(ctx context.Context, kb *agentv1alpha1.KnowledgeBase) error {
	passages, probeErr := h.probers.KnowledgeBases.Probe(ctx, kb.Name)

	// LastCheckTime is when reachability was last confirmed, so a failed probe
	// leaves it alone.
	lastCheck := kb.Status.LastCheckTime
	if probeErr == nil {
		now := metav1.Now()
		lastCheck = &now
	}

	conditions := []metav1.Condition{
		condition(kb.Status.Conditions, kb.Generation, agentv1alpha1.ConditionReachable,
			probeErr == nil,
			reasonFor(probeErr == nil, "RetrievalSucceeded", "RetrievalFailed"),
			messageOr(probeErr, fmt.Sprintf("the %s platform answered with %d passages", kb.Spec.Platform, passages))),
	}

	status := applyconfigurations.KnowledgeBaseStatus().WithConditions(applyConditions(conditions)...)
	if lastCheck != nil {
		status = status.WithLastCheckTime(*lastCheck)
	}
	apply := applyconfigurations.KnowledgeBase(kb.Name, kb.Namespace).WithStatus(status)

	_, err := h.client.Clientset.AgentV1alpha1().KnowledgeBases(kb.Namespace).ApplyStatus(
		ctx, apply, metav1.ApplyOptions{FieldManager: RuntimeFieldManager},
	)
	return err
}

// applyMCPTools converts the discovered tools into the apply shape.
func applyMCPTools(tools []agentv1alpha1.MCPTool) []*applyconfigurations.MCPToolApplyConfiguration {
	out := make([]*applyconfigurations.MCPToolApplyConfiguration, 0, len(tools))
	for i := range tools {
		tool := tools[i]
		entry := &applyconfigurations.MCPToolApplyConfiguration{Name: &tool.Name}
		if tool.Description != "" {
			entry.Description = &tool.Description
		}
		if tool.SchemaHash != "" {
			entry.SchemaHash = &tool.SchemaHash
		}
		out = append(out, entry)
	}
	return out
}

// probeModels checks every enabled model and applies its conditions.
func (h *HealthReporter) probeModels(ctx context.Context) {
	models, err := h.client.Models().List(labels.Everything())
	if err != nil {
		slog.WarnContext(ctx, "health: could not list models", "error", err)
		return
	}

	for _, model := range models {
		if !model.Spec.Enabled {
			continue
		}
		if err := h.probeModel(ctx, model); err != nil {
			slog.WarnContext(ctx, "health: could not publish model health",
				"model", model.Namespace+"/"+model.Name, "error", err)
		}
	}
}

func (h *HealthReporter) probeModel(ctx context.Context, model *agentv1alpha1.Model) error {
	start := time.Now()
	reachable, authorized, message := h.callModelsEndpoint(ctx, model)
	latency := time.Since(start)

	conditions := []metav1.Condition{
		h.condition(model, agentv1alpha1.ConditionReachable,
			reachable, reasonFor(reachable, "EndpointAnswered", "EndpointUnreachable"), message),
	}
	if model.Spec.CredentialRef != nil {
		conditions = append(conditions, h.condition(model, agentv1alpha1.ConditionAuthOK,
			authorized, reasonFor(authorized, "Authorized", "Unauthorized"),
			authMessage(authorized)))
	}

	apply := applyconfigurations.Model(model.Name, model.Namespace).
		WithStatus(applyconfigurations.ModelStatus().
			WithConditions(applyConditions(conditions)...).
			WithLatencyMillis(int32(latency.Milliseconds())).
			WithLastProbeTime(metav1.Now()))

	// Status lives on a subresource: a patch of the main resource would have its
	// status reset by the storage strategy, which is a very quiet way to lose
	// every probe result.
	_, err := h.client.Clientset.AgentV1alpha1().Models(model.Namespace).ApplyStatus(
		ctx, apply, metav1.ApplyOptions{FieldManager: RuntimeFieldManager},
	)
	return err
}

// callModelsEndpoint asks the OpenAI-compatible /models endpoint. It is the
// cheapest question that proves both reachability and whether the credential is
// accepted; a completion would cost tokens on every probe.
func (h *HealthReporter) callModelsEndpoint(ctx context.Context, model *agentv1alpha1.Model) (reachable, authorized bool, message string) {
	apiKey, err := h.credential(ctx, model)
	if err != nil {
		return false, false, err.Error()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(model.Spec.Endpoint, "/")+"/models", nil)
	if err != nil {
		return false, false, fmt.Sprintf("invalid endpoint: %v", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := h.http.Do(req)
	if err != nil {
		return false, false, fmt.Sprintf("request failed: %v", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return true, true, "the endpoint answered and accepted the credential"
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// Reachable but refuses us: the network path is fine, the credential is
		// not. Reporting unreachable here would send an operator hunting for a
		// network fault that does not exist.
		return true, false, fmt.Sprintf("the endpoint answered %d; the credential was rejected", resp.StatusCode)
	default:
		return false, false, fmt.Sprintf("the endpoint answered %d", resp.StatusCode)
	}
}

// credential reads the API key named by the model, if any.
func (h *HealthReporter) credential(ctx context.Context, model *agentv1alpha1.Model) (string, error) {
	ref := model.Spec.CredentialRef
	if ref == nil {
		return "", nil
	}
	secret, err := h.client.KubeClient.CoreV1().Secrets(model.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading credential Secret %s/%s: %w", model.Namespace, ref.Name, err)
	}
	value, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("credential Secret %s/%s has no key %q", model.Namespace, ref.Name, ref.Key)
	}
	// A model written through the API stores its key sealed. Probing with that
	// value would report a valid credential as rejected, which sends an
	// operator chasing the wrong problem.
	if agentmodel.IsSealedAPIKey(string(value)) {
		key, err := agentmodel.OpenAPIKey(string(value), h.encryptionKey)
		if err != nil {
			return "", fmt.Errorf("credential Secret %s/%s is sealed and cannot be opened: %w",
				model.Namespace, ref.Name, err)
		}
		return key, nil
	}
	return string(value), nil
}

// condition builds a condition, keeping the previous transition time when the
// status did not change so a steady probe does not rewrite the object.
func (h *HealthReporter) condition(model *agentv1alpha1.Model, conditionType string, ok bool, reason, message string) metav1.Condition {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}

	transition := metav1.Now()
	for _, existing := range model.Status.Conditions {
		if existing.Type == conditionType && existing.Status == status {
			transition = existing.LastTransitionTime
			break
		}
	}

	return metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: transition,
		ObservedGeneration: model.Generation,
	}
}

// applyConditions converts the conditions into the apply shape.
//
// The conditions list is an associative list keyed by type, so applying these
// entries leaves the controller's Ready condition untouched.
func applyConditions(conditions []metav1.Condition) []*metav1apply.ConditionApplyConfiguration {
	out := make([]*metav1apply.ConditionApplyConfiguration, 0, len(conditions))
	for i := range conditions {
		condition := conditions[i]
		out = append(out, &metav1apply.ConditionApplyConfiguration{
			Type:               &condition.Type,
			Status:             &condition.Status,
			Reason:             &condition.Reason,
			Message:            &condition.Message,
			LastTransitionTime: &condition.LastTransitionTime,
			ObservedGeneration: &condition.ObservedGeneration,
		})
	}
	return out
}

func reasonFor(ok bool, whenTrue, whenFalse string) string {
	if ok {
		return whenTrue
	}
	return whenFalse
}

func authMessage(authorized bool) string {
	if authorized {
		return "the endpoint accepted the credential"
	}
	return "the endpoint rejected the credential"
}
