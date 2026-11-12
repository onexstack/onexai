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

package controlplanestore

import (
	"encoding/json"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// Annotation keys carrying fields that have no obvious spec home.
//
// as-command changes whether a skill shows up as a /command, so it is
// spec-shaped — it stays an annotation only until the field is added to the API.
// (The record's tags are not here: they are the object's labels, which is what
// makes them selectable. See tagsFromLabels.)
const (
	annotationAsCommand = "onexai.io/as-command"
)

// agentFromCR maps an Agent object into the row shape the runtime consumes.
func agentFromCR(cr *agentv1alpha1.Agent) model.AgentM {
	agent := model.AgentM{
		Name:               cr.Name,
		DisplayName:        cr.Spec.DisplayName,
		Description:        cr.Spec.Description,
		Status:             statusOf(cr.Spec.Enabled),
		Model:              qualifiedRefName(cr.Namespace, cr.Spec.ModelRef),
		RequireApproval:    model.StringList(cr.Spec.RequireApproval),
		Webhooks:           model.StringList(cr.Spec.Webhooks),
		AuthnWebhooks:      model.StringList(cr.Spec.AuthnWebhooks),
		AuthzWebhooks:      model.StringList(cr.Spec.AuthzWebhooks),
		Namespace:          cr.Namespace,
		RuntimeProfile:     runtimeProfileName(cr.Spec.Runtime),
		OutputSchemaStrict: cr.Spec.OutputSchemaStrict,
		AuthzBackends:      jsonArray(cr.Spec.AuthzBackends),
		Labels:             labelsFrom(cr),
		CreatedAt:          unixMilli(cr.CreationTimestamp),
		UpdatedAt:          unixMilli(cr.CreationTimestamp),
	}
	agent.Skills = model.StringList(qualifiedNames(cr.Namespace, cr.Spec.Skills))
	agent.MCPs = model.StringList(qualifiedNames(cr.Namespace, cr.Spec.MCPs))
	agent.KnowledgeBases = model.StringList(qualifiedNames(cr.Namespace, cr.Spec.KnowledgeBases))
	agent.CustomCommands = model.StringList(qualifiedNames(cr.Namespace, cr.Spec.CustomCommands))
	agent.TemplateRefs = model.StringList(qualifiedNames(cr.Namespace, cr.Spec.TemplateRefs))
	agent.Delegates = model.StringList(qualifiedNames(cr.Namespace, cr.Spec.Delegates))
	agent.Kind = string(cr.Spec.Kind)
	agent.Missing = model.StringList(cr.Status.Missing)
	agent.Calls = model.StringList(qualifiedNames(cr.Namespace, cr.Spec.Calls))
	agent.ExecutionJSON = encodeJSON(executionCRToModel(cr.Spec.Execution))
	agent.ContractJSON = encodeJSON(contractCRToModel(cr.Spec.Contract))
	agent.SharingJSON = encodeJSON(sharingCRToModel(cr.Spec.Sharing))
	agent.A2AStatusJSON = encodeJSON(a2aStatusCRToModel(cr.Status.A2A))
	agent.SessionTools = cr.Spec.SessionTools
	// The Resolved* sets are what the controller made of the spec: selector
	// matches and everything inherited from aggregated agents. The declared
	// fields above stay the spec view (the admin API writes them back), and
	// the runtime prefers the resolved ones.
	agent.ResolvedCustomCommands = model.StringList(cr.Status.Resolved.CustomCommands)
	// The controller may write a same-namespace entry bare. Qualifying here is
	// what keeps "which object did the runtime use" answerable once two
	// namespaces may hold the same name.
	agent.ResolvedMCPs = model.StringList(qualifiedEntries(cr.Namespace, cr.Status.Resolved.MCPs))
	agent.ResolvedSkills = model.StringList(qualifiedEntries(cr.Namespace, cr.Status.Resolved.Skills))
	agent.ResolvedKnowledgeBases = model.StringList(qualifiedEntries(cr.Namespace, cr.Status.Resolved.KnowledgeBases))
	agent.ResolvedWebhooks = model.StringList(cr.Status.Resolved.Webhooks)
	agent.ResolvedRequireApproval = model.StringList(cr.Status.Resolved.RequireApproval)
	agent.ResolvedCommands = model.StringList(cr.Status.Resolved.Commands)
	agent.ResolvedTemplates = model.StringList(cr.Status.Resolved.Templates)
	agent.ResolvedDelegates = model.StringList(cr.Status.Resolved.Delegates)
	agent.ResolvedCalls = model.StringList(cr.Status.Resolved.Calls)
	agent.ResolvedSessionTools = cr.Status.Resolved.SessionTools
	// A status written before ModelNamespace existed carries only the bare
	// name; resolve it against the agent's own namespace. The runtime needs the
	// pair, not the bare name, once two namespaces may hold the same endpoint.
	agent.ResolvedModel = model.RefOf(
		cr.Namespace, cr.Status.Resolved.ModelNamespace, cr.Status.Resolved.Model).Canonical()
	if cr.Status.Resolved.OutputSchema != nil {
		agent.ResolvedOutputSchema = string(cr.Status.Resolved.OutputSchema.Raw)
	}
	agent.ResolvedOutputSchemaStrict = cr.Status.Resolved.OutputSchemaStrict
	if cr.Status.Resolved.Memory != nil {
		agent.ResolvedMemoryEnabled = cr.Status.Resolved.Memory.Enabled
		agent.ResolvedMemoryName = model.CanonicalRef(cr.Namespace, cr.Status.Resolved.Memory.Name)
		agent.ResolvedMemoryEmbedding = cr.Status.Resolved.Memory.EmbeddingModel
		agent.ResolvedMemoryTTLDays = cr.Status.Resolved.Memory.TTLDays
		agent.ResolvedMemoryAutoSave = cr.Status.Resolved.Memory.AutoSaveEvery
	}
	agent.Observed = cr.Status.ObservedGeneration > 0
	agent.ResolvedHash = cr.Status.ResolvedHash
	agent.BindingContributionsTotal = cr.Status.BindingContributionsTotal
	agent.BindingContributionsTruncated = cr.Status.BindingContributionsTruncated
	for _, contribution := range cr.Status.BindingContributions {
		agent.BindingContributions = append(agent.BindingContributions, model.BindingContributionM{
			Kind:               contribution.Kind,
			Namespace:          contribution.Namespace,
			Name:               contribution.Name,
			Generation:         contribution.Generation,
			Hash:               contribution.Hash,
			PromptCount:        contribution.PromptCount,
			SkillCount:         contribution.SkillCount,
			MCPCount:           contribution.MCPCount,
			KnowledgeBaseCount: contribution.KnowledgeBaseCount,
			CustomCommandCount: contribution.CustomCommandCount,
			CommandCount:       contribution.CommandCount,
		})
	}

	agent.PromptRefs = promptRefsFromCR(cr)
	if cr.Spec.OutputSchema != nil {
		agent.OutputSchema = string(cr.Spec.OutputSchema.Raw)
	}
	agent.MemoryRef = qualifiedRefName(cr.Namespace, cr.Spec.MemoryRef)
	agent.AggregatedPrompt = cr.Status.AggregatedPrompt

	if cr.Spec.Owner != nil {
		agent.Owner = cr.Spec.Owner.Name
	}
	return agent
}

// AgentToCR maps a row back onto an Agent's spec, used by the admin write path.
// Metadata (resourceVersion, labels, finalizers) is left to the existing object.
func AgentToCR(row *model.AgentM, cr *agentv1alpha1.Agent) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.ModelRef = refFromEntry(cr.Namespace, row.Model)
	cr.Spec.Skills = nameRefsFrom(cr.Namespace, row.Skills)
	cr.Spec.MCPs = nameRefsFrom(cr.Namespace, row.MCPs)
	cr.Spec.KnowledgeBases = nameRefsFrom(cr.Namespace, row.KnowledgeBases)
	cr.Spec.CustomCommands = nameRefsFrom(cr.Namespace, row.CustomCommands)
	// A nil list means the admin API had no opinion, so a templateRefs written
	// with kubectl survives an unrelated console update. A non-nil empty list is
	// the console's explicit clear after the last reference was removed.
	if row.TemplateRefs != nil {
		cr.Spec.TemplateRefs = nameRefsFrom(cr.Namespace, row.TemplateRefs)
	}
	// The admin API now carries delegates. A nil row value means "no opinion",
	// so a kubectl-only declaration survives an unrelated console update; an
	// explicit empty list is the console's clear after the last delegate was
	// removed.
	if row.Delegates != nil {
		cr.Spec.Delegates = nameRefsFrom(cr.Namespace, row.Delegates)
	}
	if row.Kind != "" {
		cr.Spec.Kind = agentv1alpha1.AgentKind(row.Kind)
	}
	// Calls is a new field; an empty row value is "no opinion" so a kubectl
	// declaration survives an unrelated console update.
	if row.Calls != nil {
		cr.Spec.Calls = nameRefsFrom(cr.Namespace, row.Calls)
	}
	cr.Spec.Execution = executionModelToCR(decodeJSON[model.AgentExecutionM](row.ExecutionJSON))
	cr.Spec.Contract = contractModelToCR(decodeJSON[model.AgentContractM](row.ContractJSON))
	cr.Spec.Sharing = sharingModelToCR(decodeJSON[model.AgentSharingM](row.SharingJSON))
	cr.Spec.RequireApproval = []string(row.RequireApproval)
	// A nil row value means "no opinion", so a hook declared with kubectl
	// survives an unrelated console update; an explicit empty list is the
	// console's clear after the last hook was removed. Hooks an aggregated
	// agent inherits live in status.resolved, never in spec, so this write
	// cannot touch them either way.
	if row.Webhooks != nil {
		cr.Spec.Webhooks = []string(row.Webhooks)
	}
	// Same nil/empty contract as Webhooks: nil leaves a kubectl declaration
	// alone, an explicit empty list is the console's clear.
	if row.AuthnWebhooks != nil {
		cr.Spec.AuthnWebhooks = []string(row.AuthnWebhooks)
	}
	if row.AuthzWebhooks != nil {
		cr.Spec.AuthzWebhooks = []string(row.AuthzWebhooks)
	}
	cr.Spec.AuthzBackends = parseJSONArray(row.AuthzBackends)

	// PromptRefs is the ordered binding list. A nil list is "no opinion" and
	// leaves the prompt group alone; a non-nil list is authoritative and
	// replaces the legacy single promptRef and the previous list. The selector
	// is never touched: the admin API cannot express one.
	if row.PromptRefs != nil {
		cr.Spec.PromptRef = nil
		cr.Spec.Prompts = nameRefsFrom(cr.Namespace, row.PromptRefs)
	}

	if row.Owner != "" {
		cr.Spec.Owner = &rbacv1.Subject{Kind: rbacv1.UserKind, Name: row.Owner}
	}
	if row.RuntimeProfile != "" {
		cr.Spec.Runtime = &agentv1alpha1.AgentRuntimeSpec{
			ProfileRef: &agentv1alpha1.Reference{Name: row.RuntimeProfile},
		}
	} else {
		cr.Spec.Runtime = nil
	}
	if row.OutputSchema != "" {
		cr.Spec.OutputSchema = &apiextensionsv1.JSON{Raw: []byte(row.OutputSchema)}
	} else {
		cr.Spec.OutputSchema = nil
	}
	cr.Spec.OutputSchemaStrict = row.OutputSchemaStrict
	if refs := nameRefsFrom(cr.Namespace, []string{row.MemoryRef}); len(refs) > 0 {
		cr.Spec.MemoryRef = &refs[0]
	} else {
		cr.Spec.MemoryRef = nil
	}
	setLabels(cr, row.Labels)
}

// runtimeProfileName reads the profile reference out of an agent spec. An empty
// spec means the deployment default, which the runtime resolver applies.
func runtimeProfileName(spec *agentv1alpha1.AgentRuntimeSpec) string {
	if spec == nil || spec.ProfileRef == nil {
		return ""
	}
	return spec.ProfileRef.Name
}

func promptFromCR(cr *agentv1alpha1.Prompt) model.PromptM {
	return model.PromptM{
		Name:        cr.Name,
		DisplayName: cr.Spec.DisplayName,
		Description: cr.Spec.Description,
		Status:      statusOf(cr.Spec.Enabled),
		Content:     cr.Spec.Template,
		DataSources: cr.Spec.DataSources,
		Namespace:   cr.Namespace,
		Labels:      labelsFrom(cr),
		CreatedAt:   unixMilli(cr.CreationTimestamp),
		UpdatedAt:   unixMilli(cr.CreationTimestamp),
	}
}

func PromptToCR(row *model.PromptM, cr *agentv1alpha1.Prompt) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.Template = row.Content
	// The wire Prompt has no dataSources field, so a console round-trip sends
	// none. A nil map must leave what the object already carries alone instead
	// of erasing it; the SQL->CRD migration still writes a non-empty one.
	if len(row.DataSources) > 0 {
		cr.Spec.DataSources = row.DataSources
	}
	setLabels(cr, row.Labels)
}

func skillFromCR(cr *agentv1alpha1.Skill) model.SkillM {
	skill := model.SkillM{
		Name:            cr.Name,
		DisplayName:     cr.Spec.DisplayName,
		Description:     cr.Spec.Description,
		Status:          statusOf(cr.Spec.Enabled),
		Provider:        cr.Spec.Provider,
		Mode:            cr.Spec.Mode,
		Contract:        cr.Spec.Contract,
		Entrypoint:      cr.Spec.Entrypoint,
		CacheTTLSeconds: int(cr.Spec.CacheTTLSeconds),
		Namespace:       cr.Namespace,
		Labels:          labelsFrom(cr),
		CreatedAt:       unixMilli(cr.CreationTimestamp),
		UpdatedAt:       unixMilli(cr.CreationTimestamp),
	}
	if cr.Spec.Params != nil {
		skill.Params = string(cr.Spec.Params.Raw)
	}
	if cr.Annotations[annotationAsCommand] == "true" {
		skill.AsCmd = "Yes"
	}
	return skill
}

func SkillToCR(row *model.SkillM, cr *agentv1alpha1.Skill) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.Provider = row.Provider
	cr.Spec.Mode = row.Mode
	cr.Spec.Contract = row.Contract
	cr.Spec.Entrypoint = row.Entrypoint
	cr.Spec.CacheTTLSeconds = int32(row.CacheTTLSeconds)
	cr.Spec.Params = nil
	if row.Params != "" {
		cr.Spec.Params = &runtime.RawExtension{Raw: []byte(row.Params)}
	}
	if cr.Annotations == nil {
		cr.Annotations = map[string]string{}
	}
	if row.AsCmd == "Yes" {
		cr.Annotations[annotationAsCommand] = "true"
	} else {
		delete(cr.Annotations, annotationAsCommand)
	}
	setLabels(cr, row.Labels)
}

func knowledgeBaseFromCR(cr *agentv1alpha1.KnowledgeBase) model.KnowledgeBaseM {
	row := model.KnowledgeBaseM{
		Name:        cr.Name,
		DisplayName: cr.Spec.DisplayName,
		Description: cr.Spec.Description,
		Status:      statusOf(cr.Spec.Enabled),
		Platform:    cr.Spec.Platform,
		DatasetID:   cr.Spec.DatasetID,
		Namespace:   cr.Namespace,
		Labels:      labelsFrom(cr),
		CreatedAt:   unixMilli(cr.CreationTimestamp),
		UpdatedAt:   unixMilli(cr.CreationTimestamp),
	}
	if cr.Spec.ScoreThreshold != nil {
		row.ScoreThreshold = *cr.Spec.ScoreThreshold
	}
	if cr.Spec.TopK != nil {
		row.TopK = int(*cr.Spec.TopK)
	}
	return row
}

func KnowledgeBaseToCR(row *model.KnowledgeBaseM, cr *agentv1alpha1.KnowledgeBase) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.Platform = row.Platform
	cr.Spec.DatasetID = row.DatasetID
	threshold := row.ScoreThreshold
	topK := int32(row.TopK)
	cr.Spec.ScoreThreshold = &threshold
	cr.Spec.TopK = &topK
	setLabels(cr, row.Labels)
}

// refName returns a reference's name, or "" for nil.
func refName(ref *agentv1alpha1.Reference) string {
	if ref == nil {
		return ""
	}
	// Cross-namespace references are not expressible in the row shape the
	// runtime consumes; the name is what it looks up, and a same-named object in
	// the referrer's namespace is what it would find.
	return ref.Name
}

// qualifiedNames renders references as canonical "namespace/name" entries. A
// reference without a namespace belongs to defaultNamespace.
//
// The runtime consumes these entries directly: a bare name would be ambiguous
// the moment two namespaces hold the same one, and resolving it against the
// wrong default is exactly the bug this spelling removes.
func qualifiedNames(defaultNamespace string, refs []agentv1alpha1.Reference) []string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Name == "" {
			continue
		}
		names = append(names, model.RefOf(defaultNamespace, ref.Namespace, ref.Name).Canonical())
	}
	return names
}

// qualifiedEntries canonicalizes entries that the controller wrote either bare
// (same namespace) or qualified (cross-namespace).
func qualifiedEntries(defaultNamespace string, entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		out = append(out, model.ResolveRef(defaultNamespace, entry).Canonical())
	}
	return out
}

// qualifiedRefName renders one reference, or "" for nil.
func qualifiedRefName(defaultNamespace string, ref *agentv1alpha1.Reference) string {
	if ref == nil || ref.Name == "" {
		return ""
	}
	return model.RefOf(defaultNamespace, ref.Namespace, ref.Name).Canonical()
}

// nameRefsFrom restores spec references from canonical entries. A reference in
// the object's own namespace is written bare, so the common case round-trips
// unchanged and an update does not introduce a namespace field nobody asked for.
// refFromEntry is nameRefsFrom for the single-valued references (model,
// memory). An empty entry means "no reference".
func refFromEntry(defaultNamespace, entry string) *agentv1alpha1.Reference {
	if entry == "" {
		return nil
	}
	refs := nameRefsFrom(defaultNamespace, []string{entry})
	if len(refs) == 0 {
		return nil
	}
	return &refs[0]
}

func nameRefsFrom(defaultNamespace string, names []string) []agentv1alpha1.Reference {
	if len(names) == 0 {
		return nil
	}
	refs := make([]agentv1alpha1.Reference, 0, len(names))
	for _, entry := range names {
		ref := model.ResolveRef(defaultNamespace, entry)
		if ref.Name == "" {
			continue
		}
		out := agentv1alpha1.Reference{Name: ref.Name}
		if ref.Namespace != model.NormalizeNamespace(defaultNamespace) {
			out.Namespace = ref.Namespace
		}
		refs = append(refs, out)
	}
	return refs
}

// promptRefsFromCR returns the ordered Prompt references the runtime falls back
// to while the aggregated Prompt is not available.
//
// The order is promptRef first (its compatibility spelling), then prompts in
// the order the spec lists them. Cross-namespace references are kept: the
// controller has already checked that the target admits this agent.
// Selector matches are deliberately absent — only the controller can list
// Prompts, and the aggregated Prompt already folds them in.
//
// An empty result means the engine's default instruction; the built-in
// sentinel is not carried in the list.
func promptRefsFromCR(cr *agentv1alpha1.Agent) model.StringList {
	if cr == nil {
		return nil
	}
	refs := make([]agentv1alpha1.Reference, 0, 1+len(cr.Spec.Prompts))
	if cr.Spec.PromptRef != nil {
		refs = append(refs, *cr.Spec.PromptRef)
	}
	refs = append(refs, cr.Spec.Prompts...)

	seen := make(map[string]bool, len(refs))
	var names model.StringList
	for _, ref := range refs {
		if ref.Name == "" || ref.Name == agentv1alpha1.BuiltinPrompt {
			continue
		}
		key := model.RefOf(cr.Namespace, ref.Namespace, ref.Name).Canonical()
		if seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, key)
	}
	return names
}

func nameRef(name string) *agentv1alpha1.Reference {
	if name == "" {
		return nil
	}
	return &agentv1alpha1.Reference{Name: name}
}

// jsonArray encodes a string list as the JSON text the model columns hold.
func jsonArray(values []string) string {
	if len(values) == 0 {
		return ""
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func parseJSONArray(text string) []string {
	if text == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(text), &values); err != nil {
		return nil
	}
	return values
}

// labelsFrom projects an object's labels onto the record's label map.
//
// It is a copy, not the map the object holds: the record outlives the object it
// was read from, and a caller that edited one must not silently edit the other.
func labelsFrom(obj metav1.Object) model.StringMap {
	labels := obj.GetLabels()
	if len(labels) == 0 {
		return nil
	}
	out := make(model.StringMap, len(labels))
	for key, value := range labels {
		out[key] = value
	}
	return out
}

// setLabels writes the record's labels onto the object.
//
// It replaces the label set rather than merging into it: on these objects the
// record's labels are the object's labels, and a merge would leave a removed
// label behind for ever. A component that wants a label of its own has to put it
// in the record — the same way a Pod's labels are the whole set its spec
// declares.
func setLabels(obj metav1.Object, labels model.StringMap) {
	if len(labels) == 0 {
		obj.SetLabels(nil)
		return
	}
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		out[key] = value
	}
	obj.SetLabels(out)
}

// LabelsFromTags converts the tag spelling the old rows still hold into a label
// set: "team=sre" for a label with a value, "onex" for one without.
//
// It exists for hack/pg2crd alone. Nothing in the running services reads tags any
// more — the column is history, and this is the one place that history is read.
func LabelsFromTags(tags []string) model.StringMap {
	if len(tags) == 0 {
		return nil
	}
	out := make(model.StringMap, len(tags))
	for _, tag := range tags {
		key, value, _ := strings.Cut(tag, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// statusOf maps the declarative enabled flag onto the row's status column.
func statusOf(enabled bool) string {
	if enabled {
		return ""
	}
	return model.StatusDisabled
}

// enabledOf is statusOf's inverse.
func enabledOf(status string) bool { return status != model.StatusDisabled }

func unixMilli(t metav1.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func webhookFromCR(cr *agentv1alpha1.Webhook) model.WebhookM {
	hook := model.WebhookM{
		Name:           cr.Name,
		Status:         statusOf(cr.Spec.Enabled),
		Type:           cr.Spec.Type,
		URL:            cr.Spec.Endpoint,
		Events:         model.StringList(cr.Spec.Events),
		Audiences:      model.StringList(cr.Spec.Audiences),
		TimeoutSeconds: int(cr.Spec.TimeoutSeconds),
		FailureMode:    cr.Spec.FailureMode,
		Headers:        model.StringMap(cr.Spec.Headers),
		Template:       cr.Spec.Template,
		Owner:          subjectName(cr.Spec.Owner),
		CreatedAt:      unixMilli(cr.CreationTimestamp),
		UpdatedAt:      unixMilli(cr.CreationTimestamp),
	}
	if cr.Spec.SecretRef != nil {
		hook.SecretRef = cr.Spec.SecretRef.Name
	}
	if cr.Spec.ClientConfig != nil {
		hook.ClientConfigJSON = encodeJSON(cr.Spec.ClientConfig)
		if cr.Spec.ClientConfig.URL != nil {
			hook.URL = *cr.Spec.ClientConfig.URL
		}
	}
	return hook
}

func WebhookToCR(row *model.WebhookM, cr *agentv1alpha1.Webhook) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.Endpoint = row.URL
	cr.Spec.ClientConfig = nil
	if row.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(row.ClientConfigJSON), cfg); err == nil {
			cr.Spec.ClientConfig = cfg
			cr.Spec.Endpoint = ""
		}
	}
	cr.Spec.Type = row.Type
	cr.Spec.Events = []string(row.Events)
	cr.Spec.Audiences = []string(row.Audiences)
	cr.Spec.TimeoutSeconds = int32(row.TimeoutSeconds)
	cr.Spec.FailureMode = row.FailureMode
	cr.Spec.Headers = map[string]string(row.Headers)
	cr.Spec.Template = row.Template
	cr.Spec.SecretRef = nil
	if row.SecretRef != "" {
		cr.Spec.SecretRef = &corev1.LocalObjectReference{Name: row.SecretRef}
	}
	if row.Owner != "" {
		cr.Spec.Owner = &rbacv1.Subject{Kind: rbacv1.UserKind, Name: row.Owner}
	}
}

// customCommandFromCR maps a CustomCommand object into the row shape the
// runtime consumes. The nested specs travel as JSON text, the same way a skill's
// provider params do.
func customCommandFromCR(cr *agentv1alpha1.CustomCommand) model.CustomCommandM {
	row := model.CustomCommandM{
		Name:           cr.Name,
		DisplayName:    cr.Spec.DisplayName,
		Description:    cr.Spec.Description,
		Status:         statusOf(cr.Spec.Enabled),
		Type:           cr.Spec.Type,
		Usage:          cr.Spec.Usage,
		TimeoutSeconds: int(cr.Spec.TimeoutSeconds),
		Namespace:      cr.Namespace,
		Labels:         labelsFrom(cr),
		CreatedAt:      unixMilli(cr.CreationTimestamp),
		UpdatedAt:      unixMilli(cr.CreationTimestamp),
	}
	if len(cr.Spec.Aliases) > 0 {
		row.Aliases = model.StringList(cr.Spec.Aliases)
	}
	row.Parameters = jsonTextOf(cr.Spec.Parameters)
	row.Prompt = jsonTextPtr(cr.Spec.Prompt)
	row.HTTP = jsonTextPtr(cr.Spec.HTTP)
	row.Tool = jsonTextPtr(cr.Spec.Tool)
	row.Output = jsonTextPtr(cr.Spec.Output)
	return row
}

// CustomCommandToCR maps a row back onto a CustomCommand's spec, used by the
// CRUD write path. It reports a decoding failure instead of writing a partially
// filled object.
func CustomCommandToCR(row *model.CustomCommandM, cr *agentv1alpha1.CustomCommand) error {
	// Start from an empty body. A command changing its type must not keep the
	// previous type's block: validation rejects two bodies, and the rejected
	// update names a field the caller never wrote.
	cr.Spec.Prompt = nil
	cr.Spec.HTTP = nil
	cr.Spec.Tool = nil
	cr.Spec.Output = nil

	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.Type = row.Type
	cr.Spec.Usage = row.Usage
	cr.Spec.TimeoutSeconds = int32(row.TimeoutSeconds)
	cr.Spec.Aliases = []string(row.Aliases)

	var params []agentv1alpha1.CommandParameter
	if err := unmarshalJSONText(row.Parameters, &params); err != nil {
		return fmt.Errorf("decoding parameters of custom command %q: %w", row.Name, err)
	}
	cr.Spec.Parameters = params
	if err := unmarshalJSONPtr(row.Prompt, &cr.Spec.Prompt); err != nil {
		return fmt.Errorf("decoding prompt of custom command %q: %w", row.Name, err)
	}
	if err := unmarshalJSONPtr(row.HTTP, &cr.Spec.HTTP); err != nil {
		return fmt.Errorf("decoding http of custom command %q: %w", row.Name, err)
	}
	if err := unmarshalJSONPtr(row.Tool, &cr.Spec.Tool); err != nil {
		return fmt.Errorf("decoding tool of custom command %q: %w", row.Name, err)
	}
	if err := unmarshalJSONPtr(row.Output, &cr.Spec.Output); err != nil {
		return fmt.Errorf("decoding output of custom command %q: %w", row.Name, err)
	}
	setLabels(cr, row.Labels)
	return nil
}

func jsonTextOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	// An empty slice marshals to "[]"; the empty string is the column's own
	// spelling of "nothing here", matching how SkillM.Params is written.
	if string(b) == "[]" || string(b) == "null" {
		return ""
	}
	return string(b)
}

func jsonTextPtr[T any](v *T) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func unmarshalJSONText(raw string, out any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

func unmarshalJSONPtr[T any](raw string, out **T) error {
	if raw == "" {
		return nil
	}
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return err
	}
	*out = &v
	return nil
}

// encodeJSON serialises a control-plane sub-object for the runtime row. A nil
// pointer and "null" both become "", which the row reads as "not set".
func encodeJSON(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" {
		return ""
	}
	return string(data)
}

// decodeJSON is the inverse of encodeJSON. A malformed value is treated as
// absent; the control-plane object remains the source of truth.
func decodeJSON[T any](raw string) *T {
	var out *T
	if err := unmarshalJSONPtr(raw, &out); err != nil {
		return nil
	}
	return out
}

// --- A2A control-plane <-> runtime projections ---

func executionCRToModel(e *agentv1alpha1.AgentExecution) *model.AgentExecutionM {
	if e == nil {
		return nil
	}
	out := &model.AgentExecutionM{Mode: string(e.Mode)}
	if a := e.A2A; a != nil {
		out.A2A = &model.A2AExecutionM{
			RuntimeRef:               refName(a.RuntimeRef),
			Endpoint:                 a.Endpoint,
			ClientConfig:             json.RawMessage(encodeJSON(a.ClientConfig)),
			Source:                   string(a.Source),
			CardURL:                  a.CardURL,
			Card:                     rawString(a.Card),
			ProtocolVersion:          a.ProtocolVersion,
			Auth:                     authCRToModel(a.Auth),
			TimeoutSeconds:           a.TimeoutSeconds,
			MaxConcurrentInvocations: a.MaxConcurrentInvocations,
			MetadataJSON:             a.MetadataJSON,
		}
	}
	return out
}

func decodeClientConfig(raw json.RawMessage) *agentv1alpha1.ClientConfig {
	if len(raw) == 0 {
		return nil
	}
	cfg := &agentv1alpha1.ClientConfig{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil
	}
	return cfg
}

func authCRToModel(a *agentv1alpha1.A2AAuth) *model.A2AAuthM {
	if a == nil {
		return nil
	}
	return &model.A2AAuthM{
		Type:               a.Type,
		APIKeyHeader:       a.APIKeyHeader,
		HeaderNames:        append([]string(nil), a.HeaderNames...),
		TokenURL:           a.TokenURL,
		ClientID:           a.ClientID,
		Scopes:             append([]string(nil), a.Scopes...),
		Audience:           a.Audience,
		ClientAuth:         a.ClientAuth,
		InsecureSkipVerify: a.InsecureSkipVerify,
	}
}

func contractCRToModel(c *agentv1alpha1.AgentContract) *model.AgentContractM {
	if c == nil {
		return nil
	}
	return &model.AgentContractM{
		AllowedSkills: append([]string(nil), c.AllowedSkills...),
		InputModes:    append([]string(nil), c.InputModes...),
		OutputModes:   append([]string(nil), c.OutputModes...),
	}
}

func sharingCRToModel(s *agentv1alpha1.AgentSharing) *model.AgentSharingM {
	if s == nil {
		return nil
	}
	return &model.AgentSharingM{AllowedNamespaces: append([]string(nil), s.AllowedNamespaces...)}
}

func a2aStatusCRToModel(s *agentv1alpha1.AgentA2AStatus) *model.AgentA2AStatusM {
	if s == nil {
		return nil
	}
	out := &model.AgentA2AStatusM{
		Phase:                s.Phase,
		ProtocolVersion:      s.ProtocolVersion,
		Transport:            s.Transport,
		ResolvedEndpoint:     s.ResolvedEndpoint,
		CardVersion:          s.CardVersion,
		CardDigest:           s.CardDigest,
		CardFetchedAt:        timeMillis(s.CardFetchedAt),
		LastSyncError:        s.LastSyncError,
		AuthReady:            s.AuthReady,
		Provider:             s.Provider,
		InputModes:           append([]string(nil), s.InputModes...),
		OutputModes:          append([]string(nil), s.OutputModes...),
		SecuritySchemes:      rawString(s.SecuritySchemes),
		Card:                 rawString(s.Card),
		ObservedRefreshToken: s.ObservedRefreshToken,
		Capabilities: model.A2ACapabilitiesM{
			Streaming:              s.Capabilities.Streaming,
			PushNotifications:      s.Capabilities.PushNotifications,
			ExtendedAgentCard:      s.Capabilities.ExtendedAgentCard,
			StateTransitionHistory: s.Capabilities.StateTransitionHistory,
		},
	}
	for _, skill := range s.Skills {
		out.Skills = append(out.Skills, model.A2ASkillM{
			ID:          skill.ID,
			Name:        skill.Name,
			Description: skill.Description,
			Tags:        append([]string(nil), skill.Tags...),
			Examples:    append([]string(nil), skill.Examples...),
			InputModes:  append([]string(nil), skill.InputModes...),
			OutputModes: append([]string(nil), skill.OutputModes...),
		})
	}
	for _, ext := range s.Extensions {
		out.Extensions = append(out.Extensions, model.A2AExtensionM{
			URI: ext.URI, Description: ext.Description, Required: ext.Required,
		})
	}
	for _, snapshot := range s.CardHistory {
		out.CardHistory = append(out.CardHistory, model.A2ACardSnapshotM{
			Version:        snapshot.Version,
			Digest:         snapshot.Digest,
			FetchedAt:      timeMillis(snapshot.FetchedAt),
			SkillCount:     snapshot.SkillCount,
			InterfaceCount: snapshot.InterfaceCount,
		})
	}
	return out
}

func executionModelToCR(e *model.AgentExecutionM) *agentv1alpha1.AgentExecution {
	if e == nil {
		return nil
	}
	out := &agentv1alpha1.AgentExecution{Mode: agentv1alpha1.AgentExecutionMode(e.Mode)}
	if a := e.A2A; a != nil {
		out.A2A = &agentv1alpha1.A2AExecution{
			RuntimeRef:               nameRef(a.RuntimeRef),
			Endpoint:                 a.Endpoint,
			ClientConfig:             decodeClientConfig(a.ClientConfig),
			Source:                   agentv1alpha1.A2ASource(a.Source),
			CardURL:                  a.CardURL,
			Card:                     rawExtension(a.Card),
			ProtocolVersion:          a.ProtocolVersion,
			Auth:                     authModelToCR(a.Auth),
			TimeoutSeconds:           a.TimeoutSeconds,
			MaxConcurrentInvocations: a.MaxConcurrentInvocations,
			MetadataJSON:             a.MetadataJSON,
		}
	}
	return out
}

func authModelToCR(a *model.A2AAuthM) *agentv1alpha1.A2AAuth {
	if a == nil {
		return nil
	}
	return &agentv1alpha1.A2AAuth{
		Type:               a.Type,
		APIKeyHeader:       a.APIKeyHeader,
		HeaderNames:        append([]string(nil), a.HeaderNames...),
		TokenURL:           a.TokenURL,
		ClientID:           a.ClientID,
		Scopes:             append([]string(nil), a.Scopes...),
		Audience:           a.Audience,
		ClientAuth:         a.ClientAuth,
		InsecureSkipVerify: a.InsecureSkipVerify,
	}
}

func contractModelToCR(c *model.AgentContractM) *agentv1alpha1.AgentContract {
	if c == nil {
		return nil
	}
	return &agentv1alpha1.AgentContract{
		AllowedSkills: append([]string(nil), c.AllowedSkills...),
		InputModes:    append([]string(nil), c.InputModes...),
		OutputModes:   append([]string(nil), c.OutputModes...),
	}
}

func sharingModelToCR(s *model.AgentSharingM) *agentv1alpha1.AgentSharing {
	if s == nil {
		return nil
	}
	return &agentv1alpha1.AgentSharing{AllowedNamespaces: append([]string(nil), s.AllowedNamespaces...)}
}

func rawString(raw *runtime.RawExtension) string {
	if raw == nil {
		return ""
	}
	return string(raw.Raw)
}

func rawExtension(raw string) *runtime.RawExtension {
	if raw == "" {
		return nil
	}
	return &runtime.RawExtension{Raw: []byte(raw)}
}

func timeMillis(t *metav1.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// subjectName returns a subject's name, or "" for nil.
func subjectName(subject *rbacv1.Subject) string {
	if subject == nil {
		return ""
	}
	return subject.Name
}
