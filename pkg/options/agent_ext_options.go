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

package options

import (
	"fmt"
	"time"
)

// PromptsConfig holds prompt fragments injected into an agent's instruction,
// for the parts of the instruction that are deployment-wide rather than
// per-agent. Per-agent personas come from the resource store instead (see
// AgentM.PromptRefs).
type PromptsConfig struct {
	// System is appended to every run's instruction. It is the place for
	// deployment-wide context the model must always have.
	System string `json:"system" mapstructure:"system"`
	// SkillPrefix is prepended to the skill middleware's system prompt,
	// overriding the built-in one when non-empty.
	SkillPrefix string `json:"skill-prefix" mapstructure:"skill-prefix"`
}

// ChatModelConfig describes one model endpoint. It is used for the auxiliary
// models (vision, reviewer) that sit beside the primary agent model: an empty
// APIKey or BaseURL inherits the corresponding field from the primary model,
// so a deployment only has to name the model it wants.
type ChatModelConfig struct {
	// Provider names the model's vendor, e.g. "openai", "deepseek", "ark",
	// "claude", "gemini", "qwen", "ollama", "qianfan". Empty infers it from
	// Model.
	Provider string `json:"provider" mapstructure:"provider"`
	// Client selects how that vendor is called:
	//
	//   - "openai-compatible" (default): the service's own client over the
	//     vendor's OpenAI-compatible endpoint, which carries retries, model
	//     fallback, multimodal content and reasoning propagation.
	//   - "native": the vendor's own SDK. Full vendor protocol support, without
	//     this service's retry and fallback layers.
	//
	// It is a choice rather than something inferred, because the two differ in
	// what they support and a deployment should decide which it wants.
	Client string `json:"client" mapstructure:"client"`
	// BaseURL is the OpenAI-compatible endpoint.
	BaseURL string `json:"base-url" mapstructure:"base-url"`
	// APIKey authenticates against BaseURL.
	APIKey string `json:"api-key" mapstructure:"api-key"`
	// Model is the model name. Empty disables the feature that needs it.
	Model string `json:"model" mapstructure:"model"`
	// Timeout bounds a single call. Zero means no extra bound.
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
	// Temperature and TopP are sampling parameters. Zero values mean "leave to
	// the server", which is why they are not given defaults.
	Temperature float64 `json:"temperature" mapstructure:"temperature"`
	TopP        float64 `json:"top-p" mapstructure:"top-p"`
}

// Enabled reports whether this optional endpoint is configured.
func (c *ChatModelConfig) Enabled() bool {
	return c != nil && c.Model != ""
}

// validate checks the settings of an auxiliary model endpoint. prefix names the
// field as it is written in a config file ("agent.vision-model"), so the message
// points at the line to fix.
//
// Only Client and Timeout are checked, because they are the two fields whose
// wrong value is visible at startup: BaseURL and APIKey may be inherited from
// the primary model, so an empty one is a valid arrangement rather than an
// omission, and requiring it would refuse a configuration that works.
func (c *ChatModelConfig) validate(prefix string) []error {
	if !c.Enabled() {
		return nil
	}
	var errs []error
	switch c.Client {
	case "", "openai-compatible", "native":
	default:
		errs = append(errs, fmt.Errorf("%s.client %q is invalid (want openai-compatible or native)", prefix, c.Client))
	}
	if c.Timeout < 0 {
		errs = append(errs, fmt.Errorf("%s.timeout must be >= 0", prefix))
	}
	return errs
}

// KnowledgeBaseOptions configures the Dify knowledge-base platform. The
// endpoint and key are deployment-wide; each knowledge-base record contributes
// its own dataset id and retrieval knobs.
type KnowledgeBaseOptions struct {
	// BaseURL is the Dify API root.
	BaseURL string `json:"base-url" mapstructure:"base-url"`
	// APIKey authenticates against BaseURL.
	APIKey string `json:"api-key" mapstructure:"api-key"`
	// TopK caps the merged result across all queried knowledge bases.
	TopK int `json:"top-k" mapstructure:"top-k"`
	// MaxChars caps the rendered result, so a verbose knowledge base cannot
	// consume the run's whole context.
	MaxChars int `json:"max-chars" mapstructure:"max-chars"`
	// Timeout bounds one retrieval request.
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
}

// Enabled reports whether the Dify platform is configured.
func (o *KnowledgeBaseOptions) Enabled() bool {
	return o != nil && o.APIKey != ""
}

// RAGFlowOptions configures the RAGFlow knowledge-base platform, which
// authenticates with a tenant-wide API key rather than per end user.
//
// # Why there is no per-user scoping here
//
// RAGFlow's HTTP API takes one key for the whole tenant: every caller of that
// key retrieves from the same corpora. There is no header this client can fill
// in from the run's subject, because the platform has no notion of one. So the
// *scope* of a run's retrieval is exactly what its agents declared in
// AgentM.KnowledgeBases and nothing narrower. A deployment that needs per-user
// scoping has to express it by which knowledge bases an agent declares, not by
// anything in this section.
type RAGFlowOptions struct {
	// BaseURL is the RAGFlow API root, e.g. "http://ragflow.example.com:9380".
	// The "/api/v1" path is appended per request.
	BaseURL string `json:"base-url" mapstructure:"base-url"`
	// APIKey authenticates against BaseURL.
	APIKey string `json:"api-key" mapstructure:"api-key"`
	// SimilarityThreshold is the minimum similarity a chunk must score when the
	// knowledge-base record does not set its own. A record's non-zero
	// scoreThreshold wins; <=0 here leaves RAGFlow's own default (0.2) in place.
	SimilarityThreshold float64 `json:"similarity-threshold" mapstructure:"similarity-threshold"`
	// VectorSimilarityWeight is the weight of vector cosine similarity against
	// term similarity. <=0 leaves RAGFlow's own default (0.3) in place.
	VectorSimilarityWeight float64 `json:"vector-similarity-weight" mapstructure:"vector-similarity-weight"`
	// RerankID names a rerank model to reorder candidates with. Empty disables
	// reranking, which is also RAGFlow's default.
	//
	// The name has to resolve on the tenant, so a value naming a model the
	// tenant has not added fails the request rather than falling back: list what
	// is available with GET /api/v1/models?type=rerank before setting it.
	RerankID string `json:"rerank-id" mapstructure:"rerank-id"`
	// Keyword asks RAGFlow to extract keywords from the question and search with
	// them as well. It costs the platform one extra model call per retrieval, so
	// it needs a chat model on the tenant.
	Keyword bool `json:"keyword" mapstructure:"keyword"`
	// DocumentNameAsSource reports the platform's document name as what the
	// model is shown for a passage.
	//
	// Off by default because the name comes from whatever wrote the documents: a
	// sync that names them after their own identifiers leaves nothing readable
	// here, and an identifier shown where a name goes is noise the model cannot
	// use. Turn it on for a corpus whose documents are titled for people — the
	// platform already returns the name per passage, so it costs nothing.
	DocumentNameAsSource bool `json:"document-name-as-source" mapstructure:"document-name-as-source"`
	// TopK caps the merged result across all queried knowledge bases. A record's
	// own topK can lower its contribution but cannot raise it above this ceiling.
	TopK int `json:"top-k" mapstructure:"top-k"`
	// MaxChars caps the response size the platform may return.
	MaxChars int `json:"max-chars" mapstructure:"max-chars"`
	// Timeout bounds one request. <=0 means 30s.
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
}

// Enabled reports whether the RAGFlow platform is configured.
func (o *RAGFlowOptions) Enabled() bool {
	return o != nil && o.BaseURL != ""
}

// K8SGPTOptions configures the k8sgpt cluster fleet: one MCP server per
// Kubernetes cluster, whose tools the agent gains and whose /v1/observability
// endpoints the REST surface exposes.
type K8SGPTOptions struct {
	// Enabled turns the fleet on. The endpoints themselves come from the
	// k8sgpt_endpoint table, which is why enabling is a separate switch from
	// configuring: a deployment can hold the endpoint rows without dialing them.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// JWTKey signs the bearer token every endpoint is called with. Empty means
	// no Authorization header, which is only correct for a fleet that does not
	// authenticate — the servers otherwise reject every call.
	JWTKey string `json:"jwt-key" mapstructure:"jwt-key"`
}

// IsEnabled reports whether the fleet is turned on. It is spelled differently
// from the other option groups' Enabled because this one has a boolean field of
// that name rather than deriving it from configuration being present.
func (o *K8SGPTOptions) IsEnabled() bool { return o != nil && o.Enabled }
