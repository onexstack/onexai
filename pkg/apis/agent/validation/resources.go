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

package validation

import (
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// KnownSkillProviders are the skill sources the runtime can fetch from.
var KnownSkillProviders = []string{"github", "gitlab", "lark_doc"}

// KnownSkillModes are the three shapes a skill can take.
var KnownSkillModes = []string{"hosted", "tool", "guide"}

// KnownMCPProtocols are the transports the MCP client speaks.
var KnownMCPProtocols = []string{"streamable-http", "sse", "stdio"}

// KnownKBDatasets are the retrieval platforms a knowledge base may point at.
var KnownKBDatasets = []string{"dify", "ragflow"}

// KnownWebhookEvents are the run-lifecycle moments a hook may subscribe to.
var KnownWebhookEvents = []string{"onStart", "onEnd"}

// KnownWebhookFailureModes are what an unreachable endpoint may mean.
var KnownWebhookFailureModes = []string{"ignore", "fail"}

// KnownWebhookTypes are what a Webhook may be used for. Empty means run.
var KnownWebhookTypes = []string{"run", "authentication", "authorization"}

// WebhookTypeRun is the default type: a run-lifecycle hook.
const WebhookTypeRun = "run"

// IsAuthWebhookType reports whether the type is one of the per-request access
// review hooks (TokenReview / SubjectAccessReview).
func IsAuthWebhookType(webhookType string) bool {
	return webhookType == "authentication" || webhookType == "authorization"
}

// ValidateModel checks a model endpoint.
func ValidateModel(spec *agent.ModelSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.Endpoint == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("endpoint"), "a model must name its endpoint"))
	} else {
		allErrs = append(allErrs, validateURL(spec.Endpoint, fldPath.Child("endpoint"))...)
	}
	if spec.Model == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("model"), "a model must name the upstream model"))
	}
	if spec.CredentialRef != nil {
		if spec.CredentialRef.Name == "" || spec.CredentialRef.Key == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("credentialRef"),
				"credentialRef must name a Secret and a key"))
		}
	}
	if spec.Params.Temperature != nil && (*spec.Params.Temperature < 0 || *spec.Params.Temperature > 2) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("params", "temperature"), *spec.Params.Temperature,
			"must be between 0 and 2"))
	}
	if spec.Params.TopP != nil && (*spec.Params.TopP < 0 || *spec.Params.TopP > 1) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("params", "topP"), *spec.Params.TopP,
			"must be between 0 and 1"))
	}
	if spec.Params.MaxTokens != nil && *spec.Params.MaxTokens < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("params", "maxTokens"), *spec.Params.MaxTokens,
			"must not be negative"))
	}
	return allErrs
}

// ValidatePrompt checks a prompt template.
func ValidatePrompt(spec *agent.PromptSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if spec.Template == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("template"), "a prompt must have a template"))
	}
	// etcd rejects an object over 1.5MiB and the apiserver has to hold the
	// encoded form plus metadata; 256KiB leaves generous headroom.
	if len(spec.Template) > 256*1024 {
		allErrs = append(allErrs, field.TooLong(fldPath.Child("template"), "", 256*1024))
	}
	if len(spec.DataSources) > 32 {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("dataSources"), len(spec.DataSources), 32))
	}
	for key, value := range spec.DataSources {
		if key == "" {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("dataSources"), key, "source key must not be empty"))
			continue
		}
		if len(value) > 64*1024 {
			allErrs = append(allErrs, field.TooLong(fldPath.Child("dataSources").Key(key), "", 64*1024))
		}
	}
	return allErrs
}

// ValidateSkill checks a skill source.
func ValidateSkill(spec *agent.SkillSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if spec.Provider == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("provider"), "a skill must name its source provider"))
	} else if !contains(KnownSkillProviders, spec.Provider) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("provider"), spec.Provider, KnownSkillProviders))
	}
	if spec.Mode != "" && !contains(KnownSkillModes, spec.Mode) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("mode"), spec.Mode, KnownSkillModes))
	}
	if spec.CacheTTLSeconds < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("cacheTTLSeconds"), spec.CacheTTLSeconds,
			"must not be negative"))
	}
	return allErrs
}

// ValidateMCPServer checks an MCP endpoint.
func ValidateMCPServer(spec *agent.MCPServerSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.Protocol == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("protocol"), "an MCP server must name its protocol"))
	} else if !contains(KnownMCPProtocols, spec.Protocol) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("protocol"), spec.Protocol, KnownMCPProtocols))
	}

	switch spec.Protocol {
	case "stdio":
		if len(spec.Command) == 0 {
			allErrs = append(allErrs, field.Required(fldPath.Child("command"),
				"the stdio protocol requires a command"))
		}
	case "sse", "streamable-http":
		switch {
		case spec.ClientConfig != nil:
			allErrs = append(allErrs, validateClientConfig(spec.ClientConfig, fldPath.Child("clientConfig"))...)
		case spec.URL == "":
			allErrs = append(allErrs, field.Required(fldPath.Child("url"),
				"the "+spec.Protocol+" protocol requires a url or clientConfig"))
		default:
			allErrs = append(allErrs, validateURL(spec.URL, fldPath.Child("url"))...)
		}
	}

	if spec.HeadersRef != nil && spec.HeadersRef.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("headersRef", "name"),
			"headersRef must name a Secret"))
	}
	return allErrs
}

// ValidateKnowledgeBase checks a retrieval dataset reference.
func ValidateKnowledgeBase(spec *agent.KnowledgeBaseSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if spec.Platform == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("platform"), "a knowledge base must name its platform"))
	} else if !contains(KnownKBDatasets, spec.Platform) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("platform"), spec.Platform, KnownKBDatasets))
	}
	if spec.DatasetID == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("datasetID"), "a knowledge base must name its dataset"))
	}
	if spec.ScoreThreshold != nil && (*spec.ScoreThreshold < 0 || *spec.ScoreThreshold > 1) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("scoreThreshold"), *spec.ScoreThreshold,
			"must be between 0 and 1"))
	}
	if spec.TopK != nil && *spec.TopK <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("topK"), *spec.TopK, "must be positive"))
	}
	return allErrs
}

// ValidateSandboxTemplate checks a sandbox image declaration.
func ValidateSandboxTemplate(spec *agent.SandboxTemplateSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if spec.Image == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("image"),
			"a sandbox template must name an image"))
	} else if !strings.Contains(spec.Image, "@sha256:") {
		// A tag can be moved under a template that already ran, which would make
		// "which image did this sandbox use" unanswerable after the fact.
		allErrs = append(allErrs, field.Invalid(fldPath.Child("image"), spec.Image,
			"the image must be pinned by digest (name@sha256:...)"))
	}
	for i, item := range spec.Env {
		if item.Name == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("env").Index(i).Child("name"),
				"an environment entry must be named"))
		}
		if item.ValueFrom != nil {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("env").Index(i).Child("valueFrom"),
				"a sandbox template cannot read a Secret: the sandbox is given no credential"))
		}
	}
	if spec.TTL != nil && spec.TTL.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("ttl"), spec.TTL.Duration, "must be positive"))
	}
	return allErrs
}

// ValidateWebhook checks a Webhook of any type.
//
// The three types share endpoint, credential and timeout, and differ in the
// fields that describe the call: a run hook has events and a payload template;
// an authentication/authorization hook is called per request with a Kubernetes
// TokenReview/SubjectAccessReview body and has neither.
func ValidateWebhook(spec *agent.WebhookSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	webhookType := spec.Type
	if webhookType == "" {
		webhookType = WebhookTypeRun
	}
	if !contains(KnownWebhookTypes, webhookType) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("type"), spec.Type, KnownWebhookTypes))
	}

	if webhookType == WebhookTypeRun {
		if len(spec.Events) == 0 {
			allErrs = append(allErrs, field.Required(fldPath.Child("events"), "a run webhook must subscribe to at least one event"))
		}
		for i, event := range spec.Events {
			if !contains(KnownWebhookEvents, event) {
				allErrs = append(allErrs, field.NotSupported(fldPath.Child("events").Index(i), event, KnownWebhookEvents))
			}
		}
	} else {
		// Per-request access review hooks are not run-lifecycle hooks. Accepting
		// events here would create a hook that looks like it fires on runs but
		// never does.
		if len(spec.Events) > 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("events"), spec.Events,
				"events are only valid for a run webhook"))
		}
		if spec.Template != "" {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("template"), "",
				"template is only valid for a run webhook; the access review body is defined by Kubernetes"))
		}
	}
	if webhookType == "authorization" && len(spec.Audiences) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("audiences"), spec.Audiences,
			"audiences are only valid for an authentication webhook"))
	}

	if spec.Endpoint == "" && spec.ClientConfig == nil {
		allErrs = append(allErrs, field.Required(fldPath.Child("clientConfig"),
			"a webhook must name its endpoint with clientConfig (or the deprecated endpoint)"))
	}
	if spec.Endpoint != "" {
		allErrs = append(allErrs, validateURL(spec.Endpoint, fldPath.Child("endpoint"))...)
	}
	if spec.ClientConfig != nil {
		allErrs = append(allErrs, validateClientConfig(spec.ClientConfig, fldPath.Child("clientConfig"))...)
	}
	if spec.SecretRef != nil && spec.SecretRef.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("secretRef", "name"), "secretRef must name a Secret"))
	}
	if spec.FailureMode != "" && !contains(KnownWebhookFailureModes, spec.FailureMode) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("failureMode"), spec.FailureMode, KnownWebhookFailureModes))
	}
	if spec.TimeoutSeconds < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("timeoutSeconds"), spec.TimeoutSeconds,
			"must not be negative"))
	}
	return allErrs
}

// validateClientConfig checks the Kubernetes-shaped endpoint configuration.
// PEM content is checked when the endpoint is resolved, not here: the API
// server should reject a malformed address, not a certificate an operator may
// rotate without touching the object.
func validateClientConfig(cfg *agent.ClientConfig, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	hasURL := cfg.URL != nil && strings.TrimSpace(*cfg.URL) != ""
	hasService := cfg.Service != nil
	switch {
	case hasURL && hasService:
		allErrs = append(allErrs, field.Invalid(fldPath, "",
			"url and service are mutually exclusive"))
	case !hasURL && !hasService:
		allErrs = append(allErrs, field.Required(fldPath.Child("url"),
			"url or service is required"))
	}
	if hasURL {
		allErrs = append(allErrs, validateURL(strings.TrimSpace(*cfg.URL), fldPath.Child("url"))...)
	}
	if hasService {
		if cfg.Service.Namespace == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("service", "namespace"),
				"service namespace is required"))
		}
		if cfg.Service.Name == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("service", "name"),
				"service name is required"))
		}
		if cfg.Service.Port != nil && (*cfg.Service.Port < 0 || *cfg.Service.Port > 65535) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("service", "port"),
				*cfg.Service.Port, "must be between 0 and 65535"))
		}
	}
	if cfg.SecretRef != nil && cfg.HeadersRef != nil {
		allErrs = append(allErrs, field.Invalid(fldPath, "",
			"secretRef and headersRef are mutually exclusive"))
	}
	if cfg.SecretRef != nil && cfg.SecretRef.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("secretRef", "name"),
			"secretRef must name a Secret"))
	}
	if cfg.HeadersRef != nil && cfg.HeadersRef.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("headersRef", "name"),
			"headersRef must name a Secret"))
	}
	if cfg.TimeoutSeconds < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("timeoutSeconds"), cfg.TimeoutSeconds,
			"must not be negative"))
	}
	return allErrs
}

// KnownMemoryRetrievalModes are the recall modes a memory may use.
var KnownMemoryRetrievalModes = []string{"hybrid", "vector", "keyword"}

// KnownMemorySubjectModes are the bucket modes a memory may use.
var KnownMemorySubjectModes = []string{"user", "session"}

// KnownMemoryPIIStrategies are how PII is handled before storage.
var KnownMemoryPIIStrategies = []string{"redact", "hash", "drop"}

// ValidateMemory checks a long-term memory declaration.
func ValidateMemory(spec *agent.MemorySpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.Enabled {
		if spec.Embedding.ModelRef == nil || spec.Embedding.ModelRef.Name == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("embedding", "modelRef", "name"),
				"an enabled memory must name an embedding model"))
		}
		if spec.Embedding.Dimensions <= 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("embedding", "dimensions"),
				spec.Embedding.Dimensions, "must be positive"))
		}
		if spec.Embedding.Version == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("embedding", "version"),
				"an enabled memory must name an embedding version"))
		}
	}
	if spec.Extraction.MaxFactsPerRun < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("extraction", "maxFactsPerRun"),
			spec.Extraction.MaxFactsPerRun, "must not be negative"))
	}
	if spec.Retention.TTLDays < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("retention", "ttlDays"),
			spec.Retention.TTLDays, "must not be negative"))
	}
	if spec.Retention.MaxEntriesPerSubject < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("retention", "maxEntriesPerSubject"),
			spec.Retention.MaxEntriesPerSubject, "must not be negative"))
	}
	if spec.Retrieval.Mode != "" && !contains(KnownMemoryRetrievalModes, spec.Retrieval.Mode) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("retrieval", "mode"),
			spec.Retrieval.Mode, KnownMemoryRetrievalModes))
	}
	if spec.Retrieval.TopK < 0 || spec.Retrieval.TopK > 100 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("retrieval", "topK"),
			spec.Retrieval.TopK, "must be between 0 and 100"))
	}
	if spec.Retrieval.MinScore < 0 || spec.Retrieval.MinScore > 1 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("retrieval", "minScore"),
			spec.Retrieval.MinScore, "must be between 0 and 1"))
	}
	if spec.Retrieval.MaxTokens < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("retrieval", "maxTokens"),
			spec.Retrieval.MaxTokens, "must not be negative"))
	}
	if spec.Security.PIIRedaction.Strategy != "" &&
		!contains(KnownMemoryPIIStrategies, spec.Security.PIIRedaction.Strategy) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("security", "piiRedaction", "strategy"),
			spec.Security.PIIRedaction.Strategy, KnownMemoryPIIStrategies))
	}
	if spec.Scope.SubjectMode != "" && !contains(KnownMemorySubjectModes, spec.Scope.SubjectMode) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("scope", "subjectMode"),
			spec.Scope.SubjectMode, KnownMemorySubjectModes))
	}
	return allErrs
}

func validateURL(raw string, fldPath *field.Path) field.ErrorList {
	parsed, err := url.Parse(raw)
	if err != nil {
		return field.ErrorList{field.Invalid(fldPath, raw, err.Error())}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return field.ErrorList{field.Invalid(fldPath, raw, "must be an http or https URL")}
	}
	if parsed.Host == "" {
		return field.ErrorList{field.Invalid(fldPath, raw, "must include a host")}
	}
	return nil
}
