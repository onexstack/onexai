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

// Package a2a adapts a remote A2A runtime to the runtime port. It is the only
// place in the agent service that knows A2A artifact/part shapes; everything
// downstream sees the domain event type.
package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	agent2a "github.com/onexstack/onexai/internal/agent/pkg/a2a"
	"github.com/onexstack/onexai/internal/agent/pkg/a2aevent"
	a2atransport "github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/a2a"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// Config describes one remote runtime endpoint.
type Config struct {
	// Name is the runtime profile name used in logs and metrics.
	Name string
	// Endpoint is the A2A base URL.
	Endpoint string
	// BearerToken authenticates the gateway to the runtime.
	BearerToken string
	// CACert is a PEM bundle used to verify the runtime.
	CACert []byte
	// InsecureSkipVerify disables TLS verification. Local development only.
	InsecureSkipVerify bool
	// HTTPClient overrides the constructed client. Tests use it to point at an
	// httptest server.
	HTTPClient *http.Client
}

// Adapter is a remote A2A runtime.
type Adapter struct {
	cfg       Config
	client    *a2aclient.Client
	transport *a2atransport.Client

	mu   sync.RWMutex
	card *a2atype.AgentCard
}

var _ runtime.Port = (*Adapter)(nil)

// New creates an adapter. It does not dial: the client is built from the
// endpoint, and the first Card or Run performs the network work.
func New(cfg Config) (*Adapter, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("a2a runtime: endpoint is required")
	}
	transport := a2atransport.New(a2atransport.Config{
		Endpoint:           cfg.Endpoint,
		BearerToken:        cfg.BearerToken,
		CACert:             cfg.CACert,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		HTTPClient:         cfg.HTTPClient,
	})
	client, err := a2aclient.NewFromEndpoints(context.Background(),
		[]*a2atype.AgentInterface{a2atype.NewAgentInterface(cfg.Endpoint, a2atype.TransportProtocolJSONRPC)},
		a2aclient.WithJSONRPCTransport(transport.HTTPClient()),
		a2aclient.WithRESTTransport(transport.HTTPClient()),
	)
	if err != nil {
		return nil, fmt.Errorf("a2a runtime %q: build client: %w", cfg.Name, err)
	}
	return &Adapter{cfg: cfg, client: client, transport: transport}, nil
}

// Name implements runtime.Port.
func (a *Adapter) Name() string {
	if a.cfg.Name != "" {
		return a.cfg.Name
	}
	return "a2a"
}

// Kind implements runtime.Port.
func (a *Adapter) Kind() string { return "a2a" }

// Card implements runtime.Port. It fetches and caches the runtime's Agent Card.
func (a *Adapter) Card(ctx context.Context) (*a2atype.AgentCard, error) {
	a.mu.RLock()
	cached := a.card
	a.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	card, err := a.fetchCard(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.card = card
	a.mu.Unlock()
	return card, nil
}

func (a *Adapter) fetchCard(ctx context.Context) (*a2atype.AgentCard, error) {
	status, body, err := a.transport.Card(ctx, a.cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("a2a runtime %q: %w", a.Name(), err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("a2a runtime %q: card answered status %d: %s",
			a.Name(), status, strings.TrimSpace(baseclientset.Truncate(body, 4<<10)))
	}
	var card a2atype.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("a2a runtime %q: decode card: %w", a.Name(), err)
	}
	return &card, nil
}

// Run implements runtime.Port.
func (a *Adapter) Run(ctx context.Context, req *runtime.Request) (<-chan agentapi.Event, error) {
	if req == nil || req.A2A == nil {
		return nil, fmt.Errorf("a2a runtime %q: nil request", a.Name())
	}
	stream := a.client.SendStreamingMessage(ctx, req.A2A)
	out := make(chan agentapi.Event, 16)
	go func() {
		defer close(out)
		state := &streamState{}
		for event, err := range stream {
			if err != nil {
				select {
				case out <- agentapi.Event{Type: agentapi.EventFailed, Err: fmt.Errorf("a2a runtime %q: %w", a.Name(), err)}:
				case <-ctx.Done():
				}
				return
			}
			for _, translated := range state.translate(event) {
				select {
				case out <- translated:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// Cancel implements runtime.Port.
func (a *Adapter) Cancel(ctx context.Context, runID string) error {
	if runID == "" {
		return nil
	}
	_, err := a.client.CancelTask(ctx, &a2atype.CancelTaskRequest{ID: a2atype.TaskID(runID)})
	if err != nil {
		return fmt.Errorf("a2a runtime %q: cancel %q: %w", a.Name(), runID, err)
	}
	return nil
}

// streamState accumulates the pieces of an A2A stream that only make sense at
// the terminal event: the answer text and the latest usage.
type streamState struct {
	answer strings.Builder
	usage  *agentapi.Usage
}

func (s *streamState) translate(event a2atype.Event) []agentapi.Event {
	switch ev := event.(type) {
	case *a2atype.TaskArtifactUpdateEvent:
		return s.artifact(ev)
	case *a2atype.TaskStatusUpdateEvent:
		return s.status(ev)
	case *a2atype.Message:
		if text := a2aevent.MessageText(ev); text != "" {
			s.answer.WriteString(text)
			return []agentapi.Event{{Type: agentapi.EventAnswer, Delta: text}}
		}
		return nil
	case *a2atype.Task:
		return s.status(&a2atype.TaskStatusUpdateEvent{
			ContextID: ev.ContextID,
			TaskID:    ev.ID,
			Status:    ev.Status,
			Metadata:  ev.Metadata,
		})
	default:
		return nil
	}
}

func (s *streamState) artifact(ev *a2atype.TaskArtifactUpdateEvent) []agentapi.Event {
	if ev.Artifact == nil {
		return nil
	}
	var events []agentapi.Event
	// A structured answer may arrive as data alone (a strict peer) or as data
	// plus the text form (this service's own executor). The text form wins when
	// both are present, and the data is only re-read when it is all there is —
	// otherwise the answer would be counted twice.
	var structured any
	hasStructured, hasText := false, false
	for _, part := range ev.Artifact.Parts {
		if part == nil {
			continue
		}
		if text, ok := part.Content.(a2atype.Text); ok {
			hasText = true
			if thought, _ := part.Metadata[agent2a.ThoughtMetadataKey].(bool); thought {
				events = append(events, agentapi.Event{Type: agentapi.EventReasoning, Delta: string(text)})
				continue
			}
			s.answer.WriteString(string(text))
			events = append(events, agentapi.Event{Type: agentapi.EventAnswer, Delta: string(text)})
			continue
		}
		if data, ok := part.Content.(a2atype.Data); ok {
			if kind, _ := part.Metadata[agent2a.PartTypeMetadataKey].(string); kind == "structured-output" {
				structured, hasStructured = data.Value, true
				continue
			}
			if step, ok := toolStep(data.Value); ok {
				events = append(events, agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{step}})
			}
		}
	}
	if hasStructured && !hasText {
		if encoded, err := json.Marshal(structured); err == nil {
			s.answer.WriteString(string(encoded))
			events = append(events, agentapi.Event{Type: agentapi.EventAnswer, Delta: string(encoded)})
		}
	}
	return events
}

func (s *streamState) status(ev *a2atype.TaskStatusUpdateEvent) []agentapi.Event {
	if usage := usageFromMetadata(ev.Metadata); usage != nil {
		s.usage = usage
	}
	switch ev.Status.State {
	case a2atype.TaskStateInputRequired, a2atype.TaskStateAuthRequired:
		return []agentapi.Event{{Type: agentapi.EventInterrupted, Interrupt: interruptFromStatus(ev.Status)}}
	case a2atype.TaskStateCompleted:
		return []agentapi.Event{{
			Type:  agentapi.EventDone,
			Final: s.answer.String(),
			Usage: s.usage,
		}}
	case a2atype.TaskStateFailed, a2atype.TaskStateRejected:
		return []agentapi.Event{{Type: agentapi.EventFailed, Err: fmt.Errorf("a2a runtime: %s", statusText(ev.Status))}}
	case a2atype.TaskStateCanceled:
		return []agentapi.Event{{Type: agentapi.EventStopped}}
	default:
		return nil
	}
}

func interruptFromStatus(status a2atype.TaskStatus) *agentapi.Interrupt {
	interrupt := &agentapi.Interrupt{}
	if status.Message == nil {
		return interrupt
	}
	interrupt.RequestID = status.Message.ID
	for _, key := range []string{agent2a.HITLExtensionURI} {
		request, ok := agent2a.DecodeHITLRequest(status.Message.Metadata[key])
		if !ok {
			continue
		}
		interrupt.RequestID = request.ID
		for _, tool := range request.Tools {
			interrupt.Requests = append(interrupt.Requests, agentapi.InterruptRequest{
				Address:   tool.ID,
				ToolName:  tool.Name,
				Question:  request.Hint,
				Kind:      agentapi.InterruptKindPermission,
				Arguments: argsJSON(tool.Args),
			})
		}
		for _, question := range request.Questions {
			interrupt.Requests = append(interrupt.Requests, agentapi.InterruptRequest{
				Address:     question.ID,
				Question:    question.Question,
				Kind:        agentapi.InterruptKindQuestion,
				Options:     question.Options,
				MultiSelect: question.MultiSelect,
			})
		}
		return interrupt
	}
	return interrupt
}

func usageFromMetadata(metadata map[string]any) *agentapi.Usage {
	raw, ok := metadata[agent2a.UsageExtensionURI]
	if !ok {
		raw, ok = metadata[agent2a.UsageMetadataKey]
	}
	if !ok {
		return nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	usage := &agentapi.Usage{
		PromptTokens:     intField(fields, "inputTokens"),
		CompletionTokens: intField(fields, "outputTokens"),
		ReasoningTokens:  intField(fields, "reasoningTokens"),
		TotalTokens:      intField(fields, "totalTokens"),
		CacheHitTokens:   intField(fields, "cachedInputTokens"),
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

func toolStep(value any) (agentapi.ToolStep, bool) {
	fields, ok := value.(map[string]any)
	if !ok {
		return agentapi.ToolStep{}, false
	}
	name := stringField(fields, "name")
	if name == "" {
		return agentapi.ToolStep{}, false
	}
	return agentapi.ToolStep{
		ID:       stringField(fields, "id"),
		Name:     name,
		Status:   stringField(fields, "status"),
		Duration: time.Duration(intField(fields, "durationMs")) * time.Millisecond,
		Tokens:   intField(fields, "tokens"),
		Result:   stringField(fields, "result"),
	}, true
}

func statusText(status a2atype.TaskStatus) string {
	if status.Message == nil {
		return string(status.State)
	}
	if text := a2aevent.MessageText(status.Message); text != "" {
		return text
	}
	return string(status.State)
}

func argsJSON(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func stringField(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}

func intField(fields map[string]any, key string) int {
	switch value := fields[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	default:
		return 0
	}
}
