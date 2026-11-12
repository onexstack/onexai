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

package chatmodel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/agent/pkg/llmhttp"
	"github.com/onexstack/onexai/internal/agent/pkg/usage"
)

// OpenAIChatModel is a minimal eino ToolCallingChatModel over an
// OpenAI-compatible /chat/completions API.
type OpenAIChatModel struct {
	baseURL       string
	apiKey        string
	model         string
	fallbackModel string
	maxRetries    int
	backoff       time.Duration
	client        *http.Client
	tools         []*schema.ToolInfo
	// noStreamUsage turns off the stream_options.include_usage request. It is
	// the negative so that the zero value keeps usage on: an upstream that
	// speaks the OpenAI streaming protocol reports usage only when asked, and a
	// model built without going through NewOpenAIChatModel would otherwise
	// silently report none.
	noStreamUsage bool
	// maxTokens is the deployment's ceiling on one completion, or 0 for none.
	// It is the last of the three sources outputCeiling consults.
	maxTokens int
}

// Ensure the interface is satisfied.
var _ model.ToolCallingChatModel = (*OpenAIChatModel)(nil)

// NewOpenAIChatModel constructs the chat model. Returns nil when baseURL is empty.
func NewOpenAIChatModel(baseURL, apiKey, modelName string) *OpenAIChatModel {
	if baseURL == "" {
		return nil
	}
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}
	return &OpenAIChatModel{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   modelName,
		client:  &http.Client{Timeout: 5 * time.Minute},
	}
}

// WithHTTPClient returns a copy that sends through client. It is how a
// user-configured endpoint gets the SSRF-guarded transport: the client decides
// which addresses may be dialled, not this package.
func (m *OpenAIChatModel) WithHTTPClient(client *http.Client) *OpenAIChatModel {
	cp := ptr.Clone(m)
	cp.client = client
	return cp
}

// WithTools returns a new instance with the given tools bound (immutable).
func (m *OpenAIChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	cp := ptr.Clone(m)
	cp.tools = tools
	return cp, nil
}

// WithRetry returns a copy configured with a fallback model and retry policy.
func (m *OpenAIChatModel) WithRetry(fallbackModel string, maxRetries int, backoff time.Duration) *OpenAIChatModel {
	cp := ptr.Clone(m)
	cp.fallbackModel = fallbackModel
	cp.maxRetries = maxRetries
	cp.backoff = backoff
	return cp
}

// WithMaxTokens returns a copy whose requests carry a max_tokens ceiling unless
// a caller overrides it. 0 (the default) sends no max_tokens at all, leaving the
// ceiling to the model — which is what every deployment did before this option
// existed, and what an upstream that rejects the field needs.
func (m *OpenAIChatModel) WithMaxTokens(n int) *OpenAIChatModel {
	cp := ptr.Clone(m)
	cp.maxTokens = n
	return cp
}

// maxOutputTokensKey is the context key for a run's output ceiling. It is an
// unexported empty struct so that no other package can collide with it.
type maxOutputTokensKey struct{}

// WithMaxOutputTokens attaches an output ceiling to ctx.
//
// The value travels on the context for the same reason the usage tracker does:
// it is decided where the caller is known — the HTTP handler, which is the last
// place a request's identity is still the service that sent it — and consumed
// two layers down, in the adapter that builds the request body. Threading it
// through the run's own types would mean a new field on every struct between
// them, for a number that no layer in between has an opinion about.
func WithMaxOutputTokens(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, maxOutputTokensKey{}, n)
}

// MaxOutputTokens returns the ceiling attached to ctx, or 0 when there is none.
func MaxOutputTokens(ctx context.Context) int {
	n, _ := ctx.Value(maxOutputTokensKey{}).(int)
	return n
}

// Generation is the per-run generation configuration the adapter applies to the
// request body. It travels on the context for the same reason the output
// ceiling does: it is decided where the run is resolved and consumed at the
// adapter, and no layer in between has an opinion about it.
type Generation struct {
	Temperature     *float64
	TopP            *float64
	ProviderOptions map[string]string
}

type generationKey struct{}

// WithGeneration attaches the run's generation configuration to ctx.
func WithGeneration(ctx context.Context, gen Generation) context.Context {
	return context.WithValue(ctx, generationKey{}, gen)
}

// GenerationFromContext returns the run's generation configuration, if any.
func GenerationFromContext(ctx context.Context) (Generation, bool) {
	gen, ok := ctx.Value(generationKey{}).(Generation)
	return gen, ok
}

// requestFields are the fields provider options may not override.
var requestFields = map[string]bool{
	"model": true, "messages": true, "stream": true, "tools": true,
	"max_tokens": true, "temperature": true, "top_p": true, "stream_options": true,
}

// applyProviderOptions merges the vendor-specific fields into an encoded
// request body. Values are decoded as JSON scalars when they look like one, so
// a numeric vendor option is not sent as a string.
func applyProviderOptions(raw []byte, opts map[string]string) ([]byte, error) {
	if len(opts) == 0 {
		return raw, nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	for k, v := range opts {
		if requestFields[k] {
			continue
		}
		var decoded any
		if json.Unmarshal([]byte(v), &decoded) == nil {
			body[k] = decoded
		} else {
			body[k] = v
		}
	}
	return json.Marshal(body)
}

// outputCeiling resolves which max_tokens to send, from the most specific
// source to the least:
//
//   - a call-time model.WithMaxTokens, which is how a caller that knows better
//     than the deployment's policy says so — the cross-turn summarizer does,
//     because a summary must not be truncated by the surface ceiling of the
//     question that triggered it;
//   - the ceiling on ctx, which is the per-surface value the handler resolved;
//   - the model's own setting, which is llm.max-tokens.
//
// Every source is optional and 0 means "not set", so the result is 0 when nobody
// capped anything — and 0 is then sent as no field at all rather than as a
// literal zero, which some APIs read as "generate nothing".
func (m *OpenAIChatModel) outputCeiling(ctx context.Context, opts *model.Options) int {
	if opts != nil && opts.MaxTokens != nil {
		return *opts.MaxTokens
	}
	if n := MaxOutputTokens(ctx); n > 0 {
		return n
	}
	return m.maxTokens
}

// WithStreamUsage returns a copy that does or does not ask the upstream to
// report token usage on a streamed call.
//
// It exists as an escape hatch, not as a preference: include_usage is a request
// field, and an upstream that validates its request body strictly rejects the
// whole call rather than ignoring the field. Turning it off costs the usage
// numbers and keeps the run working.
func (m *OpenAIChatModel) WithStreamUsage(enabled bool) *OpenAIChatModel {
	cp := ptr.Clone(m)
	cp.noStreamUsage = !enabled
	return cp
}

// models returns the model try-order (primary, then distinct fallback).
func (m *OpenAIChatModel) models() []string {
	if m.fallbackModel != "" && m.fallbackModel != m.model {
		return []string{m.model, m.fallbackModel}
	}
	return []string{m.model}
}

// wire types for the OpenAI-compatible API.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireRequest struct {
	Model    string         `json:"model"`
	Stream   bool           `json:"stream"`
	Messages []wireMessage  `json:"messages"`
	Tools    []wireToolSpec `json:"tools,omitempty"`
	// MaxTokens is omitted when there is no ceiling. omitempty is what makes 0
	// mean "unset": sending `"max_tokens": 0` is a request for a completion of no
	// tokens, not for an uncapped one.
	MaxTokens     int                `json:"max_tokens,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StreamOptions *wireStreamOptions `json:"stream_options,omitempty"`
}

// wireStreamOptions is the OpenAI stream_options block. include_usage is the
// only way a streamed call reports token usage at all: without it the upstream
// sends none, so a streamed run could not report what it consumed.
type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// wireUsage is the usage block an OpenAI-compatible response reports. The
// reasoning breakdown is a provider extension and stays zero where the upstream
// does not send it.
//
// The two cache fields are DeepSeek's (a top-level sibling of prompt_tokens,
// not a nested details block; OpenAI's own spelling of the same fact is
// `prompt_tokens_details.cached_tokens`, which this adapter does not read).
// Both stay zero for an upstream that does not report them, and DeepSeek's
// uncached price is fifty times its cached one — so the split is the difference
// between knowing a run's cost and guessing at it. See usage.Call.
type wireUsage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	// PromptCacheWriteTokens is the same family of provider extension: the part
	// of the prompt the upstream stored for reuse. Providers that bill cache
	// writes report it; the rest leave it zero.
	PromptCacheWriteTokens  int `json:"prompt_cache_write_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// toSchemaUsage converts the wire block, returning nil when the upstream
// reported nothing so that callers can leave ResponseMeta empty rather than
// publish a zeroed usage.
func toSchemaUsage(u *wireUsage) *schema.TokenUsage {
	if u == nil {
		return nil
	}
	return &schema.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		// eino's schema has one field for "the prompt was partly served from a
		// cache", so DeepSeek's hit count lands there. It is filled because
		// ResponseMeta is what any consumer outside this adapter reads, and a
		// cached call that looked uncached there would misreport the cost.
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens:     u.PromptCacheHitTokens,
			CacheWriteTokens: u.PromptCacheWriteTokens,
		},
		CompletionTokensDetails: schema.CompletionTokensDetails{
			ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
		},
	}
}

// recordUsage hands one model call's usage to the run's tracker.
//
// The tracker arrives through the context because usage is only known here, at
// the adapter, while the run that renders it lives a layer up. A context with no
// tracker records nothing: the adapter stays usable outside an agent run.
func recordUsage(ctx context.Context, u *wireUsage) {
	if u == nil {
		return
	}
	usage.FromContext(ctx).Record(usage.Call{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		ReasoningTokens:  u.CompletionTokensDetails.ReasoningTokens,
		CacheHitTokens:   u.PromptCacheHitTokens,
		CacheMissTokens:  u.PromptCacheMissTokens,
		CacheWriteTokens: u.PromptCacheWriteTokens,
	})
}

type wireToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		// Parameters is the JSON Schema of the tool's arguments. Without it the
		// model receives a zero-argument function and can never call the tool
		// with usable input, so it is omitted only when the tool declares no
		// parameters at all.
		Parameters json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// openAIContent builds the OpenAI wire content for a schema message.
//
// # Text parts matter as much as images
//
// A message with parts gets an array; a message without them gets its string.
// The array used to be assembled from Content and the image parts alone — text
// parts in UserInputMultiContent were dropped whenever the message ended up with
// no image part. That is exactly the shape the vision-extraction middleware
// produces: it removes the images and appends the description as a text part, so
// the primary model was told nothing about the picture it was supposed to
// describe. The symptom is a model answering "I did not receive any image"
// while the description sits in the message it was given.
//
// A text part equal to Content is the mirror `toUserSchemaMessage` writes (it
// puts the user's words in both); appending it again would double them. Any
// other text part is content this message gained along the way and is kept, in
// order, before the images.
func openAIContent(in *schema.Message) any {
	if len(in.UserInputMultiContent) == 0 {
		return in.Content
	}

	var images []string
	var extra []string
	for _, p := range in.UserInputMultiContent {
		switch p.Type {
		case schema.ChatMessagePartTypeImageURL:
			if p.Image != nil && p.Image.URL != nil {
				images = append(images, *p.Image.URL)
			}
		case schema.ChatMessagePartTypeText:
			if p.Text != "" && p.Text != in.Content {
				extra = append(extra, p.Text)
			}
		}
	}

	if len(images) == 0 {
		// No image survives: the message is text, and the text is Content plus
		// whatever the parts carry beyond it.
		if len(extra) == 0 {
			return in.Content
		}
		if in.Content == "" {
			return strings.Join(extra, "\n\n")
		}
		return strings.Join(append([]string{in.Content}, extra...), "\n\n")
	}

	parts := make([]map[string]any, 0, len(images)+len(extra)+1)
	if in.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": in.Content})
	}
	for _, text := range extra {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": img},
		})
	}
	return parts
}

func (m *OpenAIChatModel) buildRequest(input []*schema.Message, stream bool, modelName string, tools []*schema.ToolInfo, maxTokens int) (wireRequest, error) {
	msgs := make([]wireMessage, 0, len(input))
	for _, in := range input {
		wm := wireMessage{Role: string(in.Role), Content: openAIContent(in), ToolCallID: in.ToolCallID}
		for _, tc := range in.ToolCalls {
			w := wireToolCall{ID: tc.ID, Type: tc.Type}
			w.Function.Name = tc.Function.Name
			w.Function.Arguments = tc.Function.Arguments
			wm.ToolCalls = append(wm.ToolCalls, w)
		}
		msgs = append(msgs, wm)
	}
	req := wireRequest{Model: modelName, Stream: stream, Messages: msgs, MaxTokens: maxTokens}
	if stream && !m.noStreamUsage {
		req.StreamOptions = &wireStreamOptions{IncludeUsage: true}
	}
	for _, t := range tools {
		if t == nil {
			continue
		}
		var spec wireToolSpec
		spec.Type = "function"
		spec.Function.Name = t.Name
		spec.Function.Description = t.Desc
		if t.ParamsOneOf != nil {
			js, err := t.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return wireRequest{}, fmt.Errorf("encode parameters of tool %q: %w", t.Name, err)
			}
			if js != nil {
				raw, err := json.Marshal(js)
				if err != nil {
					return wireRequest{}, fmt.Errorf("encode parameters of tool %q: %w", t.Name, err)
				}
				spec.Function.Parameters = raw
			}
		}
		req.Tools = append(req.Tools, spec)
	}
	return req, nil
}

// commonOptions merges a call's options with the model's own bound tools.
//
// eino's adk hands the agent's tool set to the model as a *call option*
// (compose.WithChatModelOption(model.WithTools(...))), it does not call
// WithTools. A Generate/Stream that ignores its options therefore drops every
// tool the agent was given, and the model reports it cannot act — the failure
// looks like the model refusing, not like a wiring bug. Model's own bound tools
// are the base so that an agent passing no tools clears them rather than
// inheriting them.
func (m *OpenAIChatModel) commonOptions(opts []model.Option) *model.Options {
	return model.GetCommonOptions(&model.Options{Tools: m.tools}, opts...)
}

// do issues the request with retry + model fallback, returning a 2xx response.
func (m *OpenAIChatModel) do(ctx context.Context, input []*schema.Message, stream bool, opts *model.Options) (*http.Response, error) {
	return llmhttp.Do(ctx, m.client, llmhttp.Config{
		Models:     m.models(),
		MaxRetries: m.maxRetries,
		Backoff:    m.backoff,
	}, func(modelName string) (*http.Request, error) {
		wireReq, err := m.buildRequest(input, stream, modelName, opts.Tools, m.outputCeiling(ctx, opts))
		if err != nil {
			return nil, err
		}
		gen, hasGen := GenerationFromContext(ctx)
		if hasGen {
			wireReq.Temperature = gen.Temperature
			wireReq.TopP = gen.TopP
		}
		raw, err := json.Marshal(wireReq)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		if hasGen {
			raw, err = applyProviderOptions(raw, gen.ProviderOptions)
			if err != nil {
				return nil, fmt.Errorf("encode request: %w", err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if m.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+m.apiKey)
		}
		return req, nil
	})
}

// Generate performs a non-streaming completion.
func (m *OpenAIChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	resp, err := m.do(ctx, input, false, m.commonOptions(opts))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message struct {
				Content          string         `json:"content"`
				ReasoningContent string         `json:"reasoning_content"`
				ToolCalls        []wireToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *wireUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	// Usage is recorded even when the response carries no choice: a call that
	// burned tokens is a call the run paid for, whatever it returned.
	recordUsage(ctx, out.Usage)
	if len(out.Choices) == 0 {
		return schema.AssistantMessage("", nil), nil
	}
	c := out.Choices[0].Message
	var toolCalls []schema.ToolCall
	for _, tc := range c.ToolCalls {
		sc := schema.ToolCall{ID: tc.ID, Type: tc.Type, Index: tc.Index}
		sc.Function.Name = tc.Function.Name
		sc.Function.Arguments = tc.Function.Arguments
		toolCalls = append(toolCalls, sc)
	}
	message := schema.AssistantMessage(c.Content, toolCalls)
	// Reasoning is carried whether or not the caller wants it surfaced: this
	// adapter is the transport, and the engine decides what to do with it.
	message.ReasoningContent = c.ReasoningContent
	if u := toSchemaUsage(out.Usage); u != nil {
		message.ResponseMeta = &schema.ResponseMeta{Usage: u, FinishReason: out.Choices[0].FinishReason}
	}
	return message, nil
}

// Stream performs a streaming completion, emitting assistant content chunks.
//
// Tool calls arrive as deltas that must be reassembled before they are handed
// on: eino merges tool calls by index and requires a call's id, type and name to
// be atomic within a chunk, so the fragments are collected here and emitted as
// one complete message. Forwarding the raw fragments would break that contract
// for any provider that splits a name across deltas, and dropping them (as this
// method used to) leaves the agent permanently unable to call a tool.
//
// Reasoning deltas ride along the same way: a chunk's reasoning_content is
// carried on the message its content arrived with, so a reasoning-only chunk is
// simply a message whose Content is empty. The field is populated whatever the
// run asked for — surfacing it is the engine's decision, not the transport's.
func (m *OpenAIChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	resp, err := m.do(ctx, input, true, m.commonOptions(opts))
	if err != nil {
		return nil, err
	}

	sr, sw := schema.Pipe[*schema.Message](16)
	go func() {
		defer resp.Body.Close()
		defer sw.Close()

		var calls toolCallAccumulator
		// usage arrives in a trailing chunk whose choices array is empty, so it
		// is collected per chunk rather than read off whichever chunk carried
		// content.
		var reported *wireUsage
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content          string         `json:"content"`
						ReasoningContent string         `json:"reasoning_content"`
						ToolCalls        []wireToolCall `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
				Usage *wireUsage `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if chunk.Usage != nil {
				reported = chunk.Usage
			}
			for _, c := range chunk.Choices {
				for _, tc := range c.Delta.ToolCalls {
					calls.add(tc)
				}
				// One upstream chunk becomes at most one message, so content and
				// the reasoning that arrived with it stay together. A chunk
				// carrying neither is still dropped: providers open a stream with
				// a role-only delta.
				if c.Delta.Content == "" && c.Delta.ReasoningContent == "" {
					continue
				}
				message := schema.AssistantMessage(c.Delta.Content, nil)
				// Carried regardless of what the run asked for: this adapter is
				// the transport, and the engine decides whether to surface it.
				message.ReasoningContent = c.Delta.ReasoningContent
				if sw.Send(message, nil) {
					return // reader closed
				}
			}
		}
		if err := scanner.Err(); err != nil {
			sw.Send(nil, err)
			return
		}
		// Record before the tool-call message is handed on. The agent then runs
		// the tools, and the call that follows is what reveals how much context
		// those tools added — so this call has to be in the tracker by then for
		// the attribution to line up with the round that requested them.
		recordUsage(ctx, reported)
		if message := calls.message(); message != nil {
			if u := toSchemaUsage(reported); u != nil {
				message.ResponseMeta = &schema.ResponseMeta{Usage: u}
			}
			sw.Send(message, nil)
		}
	}()

	return sr, nil
}

// toolCallAccumulator rebuilds the tool calls a streaming response delivers as
// fragments. Deltas of one call share an index; the first carries the id, type
// and name, and every delta may carry an argument fragment.
type toolCallAccumulator struct {
	byIndex map[int]*wireToolCall
	order   []int
}

// add merges one delta. The id, type and name are taken from the first delta
// that carries them and never concatenated, matching how eino reads a chunk:
// those three fields are atomic, only the arguments accumulate.
func (a *toolCallAccumulator) add(tc wireToolCall) {
	key := ptr.From(tc.Index)
	if a.byIndex == nil {
		a.byIndex = make(map[int]*wireToolCall)
	}
	cur, ok := a.byIndex[key]
	if !ok {
		cur = &wireToolCall{}
		a.byIndex[key] = cur
		a.order = append(a.order, key)
	}
	if cur.ID == "" {
		cur.ID = tc.ID
	}
	if cur.Type == "" {
		cur.Type = tc.Type
	}
	if cur.Function.Name == "" {
		cur.Function.Name = tc.Function.Name
	}
	cur.Function.Arguments += tc.Function.Arguments
}

// message returns the assembled assistant message, or nil when the response
// called no tools. It carries no content and no reasoning: both were already
// streamed as their own messages, and repeating them here would duplicate the
// answer and the thinking once eino concatenates the stream.
func (a *toolCallAccumulator) message() *schema.Message {
	if len(a.order) == 0 {
		return nil
	}
	sort.Ints(a.order)
	calls := make([]schema.ToolCall, 0, len(a.order))
	for _, key := range a.order {
		w := a.byIndex[key]
		index := key
		sc := schema.ToolCall{ID: w.ID, Type: w.Type, Index: &index}
		sc.Function.Name = w.Function.Name
		sc.Function.Arguments = w.Function.Arguments
		calls = append(calls, sc)
	}
	return schema.AssistantMessage("", calls)
}
