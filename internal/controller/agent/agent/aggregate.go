// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/tokenizer"
	"github.com/onexstack/onexai/internal/controller/support"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// aggregationPromptVersion is part of the input hash: changing the merge
// instructions invalidates every cached aggregate.
const aggregationPromptVersion = "v1"

// maxGeneratedPromptBytes mirrors the Prompt API's template cap. A generated
// object above it would be rejected by the API server.
const maxGeneratedPromptBytes = 256 * 1024

// aggregateOutcome is the part of an agent's status that describes the derived
// prompt, plus a condition explaining how it got there.
type aggregateOutcome struct {
	Name          string
	InputHash     string
	ContentHash   string
	Overridden    bool
	Stale         bool
	ObservedToken string
	Condition     metav1.Condition
	RequeueAfter  time.Duration
}

// buildResult is one attempt at producing the aggregate content.
type buildResult struct {
	Content    string
	Variables  []string
	Condition  metav1.Condition
	Fallback   bool
	OverBudget bool
}

// reconcileAggregatedPrompt derives, preserves, or removes the agent's
// aggregated Prompt. It never calls the model for an overridden, unavailable,
// or unchanged object.
func (r *Reconciler) reconcileAggregatedPrompt(ctx context.Context, agent *agentv1alpha1.Agent, set promptSet) (aggregateOutcome, error) {
	name := agentv1alpha1.AggregatedPromptName(agent.Name)
	if len(name) > 253 {
		return aggregateOutcome{Condition: promptCondition(agent, false, "NameTooLong",
			fmt.Sprintf("aggregated prompt name %q exceeds 253 characters", name))}, nil
	}
	if agent.DeletionTimestamp != nil {
		return aggregateOutcome{}, nil
	}

	token := ""
	if agent.Spec.PromptAggregation != nil {
		token = agent.Spec.PromptAggregation.RegenerateToken
	}
	force := token != "" && token != agent.Status.ObservedRegenerateToken

	existing := &agentv1alpha1.Prompt{}
	err := r.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: name}, existing)
	exists := err == nil
	if err != nil && client.IgnoreNotFound(err) != nil {
		return aggregateOutcome{}, err
	}

	// Remove an inline Prompt the agent no longer references. The source set
	// was resolved before this call, so a still-bound inline Prompt is kept.
	if err := r.cleanupInlinePrompt(ctx, agent, set); err != nil {
		return aggregateOutcome{}, err
	}

	if set.tooMany {
		condition := promptCondition(agent, false, "TooManySources",
			fmt.Sprintf("prompt sources exceed the limit of %d", maxPromptSources))
		if exists {
			return keepPrevious(agent, existing, condition), nil
		}
		return aggregateOutcome{
			ObservedToken: agent.Status.ObservedRegenerateToken,
			Condition:     condition,
			RequeueAfter:  30 * time.Second,
		}, nil
	}

	if len(set.declared) == 0 {
		if exists && set.sourceUnavailable {
			// The only sources were aggregated agents, and one is unavailable.
			// Keep the last good aggregate; the next successful reconcile
			// replaces it.
			return keepPrevious(agent, existing,
				promptCondition(agent, false, "SourceUnavailable", "an aggregated agent is missing or disabled")), nil
		}
		if exists && aggregatedOverridden(existing, agent) {
			// Keep the manual version; the user decides whether to delete it.
			return aggregateOutcome{
				Name:          name,
				InputHash:     existing.Annotations[agentv1alpha1.AnnotationAggregatedInputHash],
				ContentHash:   existing.Annotations[agentv1alpha1.AnnotationAggregatedContentHash],
				Overridden:    true,
				Stale:         true,
				ObservedToken: token,
				Condition:     promptCondition(agent, false, "NoPromptSources", "no prompt is bound to this agent"),
			}, nil
		}
		if exists {
			if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				return aggregateOutcome{}, err
			}
		}
		return aggregateOutcome{
			ObservedToken: token,
			Condition:     promptCondition(agent, false, "NoPromptSources", "no prompt is bound to this agent"),
		}, nil
	}

	hasUnavailable := len(set.available) != len(set.declared) || set.sourceUnavailable

	// A single source is copied verbatim and needs no endpoint. A merge does.
	var model aggregationModel
	var modelCond metav1.Condition
	modelIdentity := ""
	if len(set.available) > 1 {
		model, modelCond, err = r.resolveAggregationModel(ctx, agent)
		if err != nil {
			return aggregateOutcome{}, err
		}
		if modelCond.Status == metav1.ConditionFalse {
			modelIdentity = "missing|" + configuredModelName(agent)
		} else {
			modelIdentity = model.identity()
		}
	}
	modelUnavailable := len(set.available) > 1 && modelCond.Status == metav1.ConditionFalse

	inputHash := hashPromptInputs(set, modelIdentity, support.PromptAggregation().MaxTokens)

	if !exists {
		if len(set.available) == 0 {
			return aggregateOutcome{
				ObservedToken: token,
				Condition:     promptCondition(agent, false, "SourceUnavailable", "none of the bound prompts is available"),
				RequeueAfter:  30 * time.Second,
			}, nil
		}

		built := limitGeneratedPrompt(agent, r.buildAggregate(ctx, agent, set, model, modelCond))
		if built.Content == "" {
			return aggregateOutcome{ObservedToken: agent.Status.ObservedRegenerateToken, Condition: built.Condition, RequeueAfter: 30 * time.Second}, nil
		}
		degraded := built.Fallback || built.OverBudget
		if err := r.upsertAggregatedPrompt(ctx, agent, nil, set, built.Content, built.Variables, inputHash, degraded); err != nil {
			return aggregateOutcome{}, err
		}
		condition := built.Condition
		if hasUnavailable {
			condition = promptCondition(agent, false, "SourceUnavailable", "some bound prompts are missing or disabled")
		}
		requeue := time.Duration(0)
		if degraded || hasUnavailable || modelUnavailable || condition.Status == metav1.ConditionFalse {
			requeue = 30 * time.Second
		}
		return aggregateOutcome{
			Name:          name,
			InputHash:     inputHash,
			ContentHash:   hashPromptContent(true, built.Content, built.Variables),
			Stale:         hasUnavailable || degraded,
			ObservedToken: token,
			Condition:     condition,
			RequeueAfter:  requeue,
		}, nil
	}

	if !aggregatedOwnedBy(existing, agent) {
		return aggregateOutcome{
			Name:          "",
			ObservedToken: agent.Status.ObservedRegenerateToken,
			Condition: promptCondition(agent, false, "PromptNameConflict",
				fmt.Sprintf("prompt %q exists but is not owned by agent %q", name, agent.Name)),
		}, nil
	}

	managedContentHash := existing.Annotations[agentv1alpha1.AnnotationAggregatedContentHash]
	managedInputHash := existing.Annotations[agentv1alpha1.AnnotationAggregatedInputHash]
	overridden := aggregatedOverridden(existing, agent)

	if hasUnavailable || modelUnavailable {
		condition := promptCondition(agent, false, "SourceUnavailable", "some bound prompts are missing or disabled")
		if !hasUnavailable && modelUnavailable {
			condition = modelCond
		}
		return keepPrevious(agent, existing, condition), nil
	}

	if overridden && !force {
		stale := inputHash != managedInputHash
		reason := "Overridden"
		message := "the aggregated prompt was edited outside the controller"
		if stale {
			reason = "OverriddenStale"
			message += "; the bound prompts changed since the edit"
		}
		return aggregateOutcome{
			Name:          name,
			InputHash:     managedInputHash,
			ContentHash:   managedContentHash,
			Overridden:    true,
			Stale:         stale,
			ObservedToken: agent.Status.ObservedRegenerateToken,
			Condition:     promptCondition(agent, true, reason, message),
		}, nil
	}

	if !overridden && !force && inputHash == managedInputHash {
		reason := "Aggregated"
		if len(set.available) == 1 {
			reason = "SinglePromptCopied"
		}
		return aggregateOutcome{
			Name:          name,
			InputHash:     inputHash,
			ContentHash:   managedContentHash,
			ObservedToken: token,
			Condition:     promptCondition(agent, true, reason, "the aggregated prompt is current"),
		}, nil
	}

	// Regenerate: managed with changed inputs, or an explicit token. An empty
	// or degraded result never replaces a good previous one.
	built := r.buildAggregate(ctx, agent, set, model, modelCond)
	if built.Content == "" || built.Fallback || built.OverBudget {
		return keepPrevious(agent, existing, built.Condition), nil
	}
	if err := r.upsertAggregatedPrompt(ctx, agent, existing, set, built.Content, built.Variables, inputHash, built.Fallback || built.OverBudget); err != nil {
		return aggregateOutcome{}, err
	}
	return aggregateOutcome{
		Name:          name,
		InputHash:     inputHash,
		ContentHash:   hashPromptContent(true, built.Content, built.Variables),
		Overridden:    false,
		Stale:         false,
		ObservedToken: token,
		Condition:     built.Condition,
	}, nil
}

// keepPrevious is the answer for every state where the controller must not
// write: the last written object, its hashes and its override flag carry
// through, the status is marked stale, and the reconcile asks to be retried.
//
// It is one helper rather than five copies because the copies drift quietly.
// Two of the fields are load-bearing. The token stays unobserved, so a
// regenerate request that could not be honoured survives to the next attempt.
// The hashes are the ones on the object, so a stale marker never gets mistaken
// for a fresh write.
func keepPrevious(agent *agentv1alpha1.Agent, existing *agentv1alpha1.Prompt, condition metav1.Condition) aggregateOutcome {
	return aggregateOutcome{
		Name:          existing.Name,
		InputHash:     existing.Annotations[agentv1alpha1.AnnotationAggregatedInputHash],
		ContentHash:   existing.Annotations[agentv1alpha1.AnnotationAggregatedContentHash],
		Overridden:    aggregatedOverridden(existing, agent),
		Stale:         true,
		ObservedToken: agent.Status.ObservedRegenerateToken,
		Condition:     condition,
		RequeueAfter:  30 * time.Second,
	}
}

// buildAggregate produces the content: a verbatim copy for one source, an LLM
// merge for several, and a deterministic concatenation when the model cannot
// be used.
func (r *Reconciler) buildAggregate(ctx context.Context, agent *agentv1alpha1.Agent, set promptSet, model aggregationModel, modelCond metav1.Condition) buildResult {
	if len(set.available) == 0 {
		return buildResult{Condition: promptCondition(agent, false, "SourceUnavailable", "none of the bound prompts is available")}
	}
	if len(set.available) == 1 {
		content := strings.TrimSpace(set.available[0].Template)
		if content == "" {
			return buildResult{Condition: promptCondition(agent, false, "SourceUnavailable", "the bound prompt is empty")}
		}
		return buildResult{
			Content:   content,
			Variables: unionVariables(set.available),
			Condition: promptCondition(agent, true, "SinglePromptCopied", "a single bound prompt was copied verbatim"),
		}
	}
	if modelCond.Status == metav1.ConditionFalse {
		return buildResult{
			Content:   fallbackConcat(set.available),
			Variables: unionVariables(set.available),
			Fallback:  true,
			Condition: modelCond,
		}
	}

	chatModel := model.chatModel()
	if chatModel == nil {
		return buildResult{
			Content:   fallbackConcat(set.available),
			Variables: unionVariables(set.available),
			Fallback:  true,
			Condition: promptCondition(agent, false, "ModelMissing", "the aggregation model is not usable"),
		}
	}

	content, err := r.mergeWithModel(ctx, chatModel, model, set.available, false)
	if err != nil {
		return buildResult{
			Content:   fallbackConcat(set.available),
			Variables: unionVariables(set.available),
			Fallback:  true,
			Condition: promptCondition(agent, false, "AggregationFailed", err.Error()),
		}
	}
	if strings.TrimSpace(content) == "" {
		return buildResult{
			Content:   fallbackConcat(set.available),
			Variables: unionVariables(set.available),
			Fallback:  true,
			Condition: promptCondition(agent, false, "AggregationFailed", "the model returned an empty prompt"),
		}
	}

	variables := unionVariables(set.available)
	if tokens, countErr := tokenizer.Count(content); countErr == nil && tokens > model.MaxTokens {
		if retry, retryErr := r.mergeWithModel(ctx, chatModel, model, set.available, true); retryErr == nil && strings.TrimSpace(retry) != "" {
			content = retry
		}
		if tokens, countErr := tokenizer.Count(content); countErr == nil && tokens > model.MaxTokens {
			return buildResult{
				Content:    content,
				Variables:  variables,
				OverBudget: true,
				Condition: promptCondition(agent, false, "OverBudget",
					fmt.Sprintf("the merged prompt is %d tokens, over the %d target", tokens, model.MaxTokens)),
			}
		}
	}

	return buildResult{
		Content:   content,
		Variables: variables,
		Condition: promptCondition(agent, true, "Aggregated", "the bound prompts were merged"),
	}
}

// limitGeneratedPrompt turns an over-cap result into a TooLarge failure. The
// caller then keeps the previous content, or creates nothing.
func limitGeneratedPrompt(agent *agentv1alpha1.Agent, built buildResult) buildResult {
	if built.Content == "" || len(built.Content) <= maxGeneratedPromptBytes {
		return built
	}
	return buildResult{Condition: promptCondition(agent, false, "TooLarge",
		fmt.Sprintf("the generated prompt is %d bytes, over the %d cap", len(built.Content), maxGeneratedPromptBytes))}
}

// mergeWithModel merges the sources into one prompt. A small input is sent in
// one call; a large one is folded one source at a time so no single request
// carries the whole set.
func (r *Reconciler) mergeWithModel(ctx context.Context, chatModel *chatmodel.OpenAIChatModel, model aggregationModel, sources []promptSource, stricter bool) (string, error) {
	system := aggregationSystemPrompt(model.MaxTokens)
	if stricter {
		system += "\n\nThis is a retry: the previous result was over budget. Compress wording harder, but preserve every requirement."
	}

	total := 0
	for _, source := range sources {
		n, err := tokenizer.Count(source.Template)
		if err != nil {
			total = -1
			break
		}
		total += n
	}

	if total >= 0 && total <= model.MaxInputTokens {
		return r.generateMerge(ctx, chatModel, system, sources)
	}

	acc := promptSource{Name: sources[0].Name, Template: sources[0].Template, Variables: sources[0].Variables}
	for i := 1; i < len(sources); i++ {
		merged, err := r.generateMerge(ctx, chatModel, system, []promptSource{acc, sources[i]})
		if err != nil {
			return "", err
		}
		acc = promptSource{
			Name:      "merged",
			Template:  merged,
			Variables: unionVariables([]promptSource{acc, sources[i]}),
		}
	}
	return acc.Template, nil
}

func (r *Reconciler) generateMerge(ctx context.Context, chatModel *chatmodel.OpenAIChatModel, system string, sources []promptSource) (string, error) {
	// The call is bounded even though the controller's context is not: a model
	// that never answers would otherwise hold this worker, and every agent in
	// the namespace waits behind it.
	callCtx, cancel := context.WithTimeout(ctx, support.PromptAggregation().Timeout)
	defer cancel()

	message, err := chatModel.Generate(callCtx, []*schema.Message{
		schema.SystemMessage(system),
		schema.UserMessage(renderSources(sources)),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(message.Content), nil
}

func aggregationSystemPrompt(maxTokens int) string {
	return fmt.Sprintf(`You merge multiple system prompts for one AI agent into a single system prompt.

Rules:
1. Preserve every requirement, constraint, refusal rule, safety instruction, formatting rule, and definition from every input. Do not weaken, soften, or omit any of them.
2. Do not invent requirements that are not present in the inputs.
3. When inputs conflict, keep the stricter and more specific rule and state it once.
4. Preserve placeholders, variable names, XML tags, tool names, URLs, code blocks, and examples exactly.
5. Remove only duplication and redundant phrasing. Never paraphrase away precision.
6. Write the result in the dominant language of the inputs.
7. Output only the merged prompt. No preamble, no explanation, and no code fence around the whole answer.
8. Target at most %d tokens. Compress wording, never requirements.`, maxTokens)
}

func renderSources(sources []promptSource) string {
	type wireSource struct {
		Name      string   `json:"name"`
		Variables []string `json:"variables,omitempty"`
		Template  string   `json:"template"`
	}
	wire := make([]wireSource, 0, len(sources))
	for _, source := range sources {
		wire = append(wire, wireSource{Name: source.Name, Variables: source.Variables, Template: source.Template})
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// fallbackConcat is the deterministic merge used when no model is usable. It
// keeps every source's text under its own heading.
func fallbackConcat(sources []promptSource) string {
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, "## "+source.Name+"\n"+strings.TrimSpace(source.Template))
	}
	return strings.Join(parts, "\n\n")
}

func unionVariables(sources []promptSource) []string {
	seen := make(map[string]bool)
	var out []string
	for _, source := range sources {
		for _, variable := range source.Variables {
			if variable == "" || seen[variable] {
				continue
			}
			seen[variable] = true
			out = append(out, variable)
		}
	}
	return out
}

// upsertAggregatedPrompt writes the content and both hashes in one update.
func (r *Reconciler) upsertAggregatedPrompt(ctx context.Context, agent *agentv1alpha1.Agent, existing *agentv1alpha1.Prompt, set promptSet, content string, variables []string, inputHash string, degraded bool) error {
	if len(content) > maxGeneratedPromptBytes {
		return fmt.Errorf("generated prompt is %d bytes, over the %d cap", len(content), maxGeneratedPromptBytes)
	}
	name := agentv1alpha1.AggregatedPromptName(agent.Name)
	prompt := existing
	if prompt == nil {
		prompt = &agentv1alpha1.Prompt{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: agent.Namespace},
		}
	} else if !aggregatedOwnedBy(prompt, agent) {
		return fmt.Errorf("prompt %q is not owned by agent %q", name, agent.Name)
	}

	prompt.OwnerReferences = []metav1.OwnerReference{agentv1alpha1.AgentOwnerReference(agent.Name, agent.UID)}
	if prompt.Annotations == nil {
		prompt.Annotations = map[string]string{}
	}
	prompt.Annotations[agentv1alpha1.AnnotationAggregatedFor] = agent.Name
	prompt.Annotations[agentv1alpha1.AnnotationAggregatedContentHash] = hashPromptContent(true, content, variables)
	if degraded {
		// A degraded result (fallback or over budget) must not look current on
		// the next reconcile: without the input hash the controller retries.
		delete(prompt.Annotations, agentv1alpha1.AnnotationAggregatedInputHash)
	} else {
		prompt.Annotations[agentv1alpha1.AnnotationAggregatedInputHash] = inputHash
	}
	prompt.Spec.Enabled = true
	prompt.Spec.Template = content
	prompt.Spec.Variables = variables
	prompt.Spec.DisplayName = "Aggregated prompt for " + agent.Name
	prompt.Spec.Description = "Derived from prompts: " + strings.Join(set.availableNames(), ", ")

	if existing == nil {
		return r.Create(ctx, prompt)
	}
	return r.Update(ctx, prompt)
}

// cleanupInlinePrompt removes the materialized inline Prompt once the agent no
// longer references it. A still-bound source or a foreign object is left alone.
//
// The object is also recognized when the SQL->CRD migration left it unowned
// (pg2crd emits a static manifest with no Agent UID to own it with). The admin
// write path used to adopt such an object on its first update; inline text is
// gone from that API now, so this is what reclaims it once it is unbound.
func (r *Reconciler) cleanupInlinePrompt(ctx context.Context, agent *agentv1alpha1.Agent, set promptSet) error {
	name := agentv1alpha1.InlinePromptName(agent.Name)
	for _, ref := range set.declared {
		if ref.Name == name && ref.Namespace == agent.Namespace {
			return nil
		}
	}
	prompt := &agentv1alpha1.Prompt{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: name}, prompt); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return err
	}
	if !ownedByAgentName(prompt, agent.Name) && !reclaimableInlinePrompt(prompt, agent.Name) {
		return nil
	}
	if err := r.Delete(ctx, prompt); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func aggregatedOwnedBy(prompt *agentv1alpha1.Prompt, agent *agentv1alpha1.Agent) bool {
	return ownedByAgentName(prompt, agent.Name)
}

func aggregatedOverridden(prompt *agentv1alpha1.Prompt, agent *agentv1alpha1.Agent) bool {
	managed := prompt.Annotations[agentv1alpha1.AnnotationAggregatedContentHash]
	current := hashPromptContent(prompt.Spec.Enabled, prompt.Spec.Template, prompt.Spec.Variables)
	if managed == "" {
		return current != agent.Status.AggregatedPromptHash
	}
	return current != managed
}

func promptCondition(agent *agentv1alpha1.Agent, ok bool, reason, message string) metav1.Condition {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{
		Type:               agentv1alpha1.ConditionPromptAggregated,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: agent.Generation,
	}
}

func hashPromptContent(enabled bool, template string, variables []string) string {
	payload := struct {
		Enabled   bool
		Template  string
		Variables []string
	}{enabled, template, variables}
	return sha256JSON(payload)
}

func hashPromptInputs(set promptSet, modelIdentity string, maxTokens int) string {
	type sourceHash struct {
		Namespace    string
		Name         string
		Available    bool
		TemplateHash string
		Variables    []string
	}
	available := make(map[string]promptSource, len(set.available))
	for _, source := range set.available {
		available[source.Namespace+"/"+source.Name] = source
	}
	sources := make([]sourceHash, 0, len(set.declared))
	for _, ref := range set.declared {
		key := ref.Namespace + "/" + ref.Name
		source, ok := available[key]
		sources = append(sources, sourceHash{
			Namespace:    ref.Namespace,
			Name:         ref.Name,
			Available:    ok,
			TemplateHash: hashPromptContent(true, source.Template, source.Variables),
			Variables:    source.Variables,
		})
	}
	payload := struct {
		Sources           []sourceHash
		Model             string
		MaxTokens         int
		Version           string
		SourceUnavailable bool
	}{sources, modelIdentity, maxTokens, aggregationPromptVersion, set.sourceUnavailable}
	return sha256JSON(payload)
}

func sha256JSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func configuredModelName(agent *agentv1alpha1.Agent) string {
	if agent.Spec.ModelRef != nil {
		return agent.Spec.ModelRef.Name
	}
	return support.PromptAggregation().Model
}
