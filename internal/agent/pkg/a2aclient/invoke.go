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

package a2aclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Task states, normalized to one spelling for 0.3 and 1.0.
const (
	StateSubmitted     = "submitted"
	StateWorking       = "working"
	StateInputRequired = "input-required"
	StateAuthRequired  = "auth-required"
	StateCompleted     = "completed"
	StateFailed        = "failed"
	StateCanceled      = "canceled"
	StateRejected      = "rejected"
	StateUnknown       = "unknown"
)

// Terminal reports a state no further message can restart.
func Terminal(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateCanceled, StateRejected:
		return true
	default:
		return false
	}
}

// Request is one message to an agent.
type Request struct {
	Message   string         `json:"message"`
	ContextID string         `json:"contextId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Skill     string         `json:"skill,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Part is one artifact part, normalized across 0.3 and 1.0.
type Part struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	FileURL  string `json:"fileUrl,omitempty"`
	FileName string `json:"fileName,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// Artifact is one output artifact.
type Artifact struct {
	ID          string `json:"artifactId"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parts       []Part `json:"parts,omitempty"`
}

// Result is the terminal (or interrupt) state of one call.
type Result struct {
	State     string     `json:"state"`
	TaskID    string     `json:"taskId,omitempty"`
	ContextID string     `json:"contextId,omitempty"`
	Text      string     `json:"text,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Message   string     `json:"message,omitempty"` // status/input-required message
	RawTask   string     `json:"rawTask,omitempty"`
}

// StreamEvent is one normalized stream event.
type StreamEvent struct {
	Type      string    `json:"type"` // status|delta|artifact|interrupt|final|error
	State     string    `json:"state,omitempty"`
	TaskID    string    `json:"taskId,omitempty"`
	ContextID string    `json:"contextId,omitempty"`
	Text      string    `json:"text,omitempty"`
	Artifact  *Artifact `json:"artifact,omitempty"`
	// Artifacts carries the whole set when a peer answers a stream with one
	// task snapshot instead of per-artifact updates. Without it the artifacts
	// exist only in the raw payload and the invocation record misses them.
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Message   string     `json:"message,omitempty"`
	Final     bool       `json:"final,omitempty"`
	Raw       string     `json:"raw,omitempty"`
}

// Invoker speaks the A2A JSON-RPC wire for one endpoint.
type Invoker struct {
	endpoint  string
	version   string
	transport string
	client    *http.Client
	headers   map[string]string
}

// NewInvoker builds an invoker for one card interface.
func NewInvoker(endpoint, version, transport string, client *http.Client, headers map[string]string) *Invoker {
	return &Invoker{
		endpoint:  strings.TrimSuffix(endpoint, "/"),
		version:   NormalizeVersion(version),
		transport: transport,
		client:    client,
		headers:   headers,
	}
}

// NormalizeVersion maps 0.3.x / 1.0.x to the two protocol families.
func NormalizeVersion(version string) string {
	switch {
	case strings.HasPrefix(version, "1."):
		return ProtocolVersion10
	case strings.HasPrefix(version, "0."):
		return ProtocolVersion03
	default:
		return ProtocolVersion03
	}
}

// Send performs one blocking message/send.
func (i *Invoker) Send(ctx context.Context, req Request) (*Result, error) {
	if i == nil || i.client == nil || i.endpoint == "" {
		return nil, errors.New("a2a invoker: endpoint and http client are required")
	}
	body, err := i.requestBody(req, false)
	if err != nil {
		return nil, err
	}
	result, err := i.call(ctx, i.methodSend(), body)
	if err != nil {
		return nil, err
	}
	parsed := parseWireResult(result)
	if parsed == nil {
		return nil, fmt.Errorf("a2a invoker: unreadable result: %s", truncate(string(result), 512))
	}
	return parsed, nil
}

// Stream performs one streaming message and emits normalized events.
func (i *Invoker) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	if i == nil || i.client == nil || i.endpoint == "" {
		return nil, errors.New("a2a invoker: endpoint and http client are required")
	}
	body, err := i.requestBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := i.newHTTPRequest(ctx, i.methodStream(), body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := i.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close() //nolint:errcheck // error path
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("a2a stream: status %d: %s", resp.StatusCode, truncate(string(payload), 512))
	}

	out := make(chan StreamEvent, 16)
	go i.readStream(ctx, resp.Body, out)
	return out, nil
}

func (i *Invoker) readStream(ctx context.Context, body io.ReadCloser, out chan<- StreamEvent) {
	defer close(out)
	defer body.Close() //nolint:errcheck // read-only body

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	var data bytes.Buffer
	emit := func(payload []byte) bool {
		event, terminal, err := parseWireEvent(payload)
		if err != nil {
			select {
			case out <- StreamEvent{Type: "error", Message: err.Error(), Raw: string(payload)}:
			case <-ctx.Done():
				return false
			}
			return false
		}
		if event == nil {
			return true
		}
		select {
		case out <- *event:
		case <-ctx.Done():
			return false
		}
		return !terminal
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				if !emit(bytes.TrimSpace(data.Bytes())) {
					return
				}
				data.Reset()
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if data.Len() > 0 {
		_ = emit(bytes.TrimSpace(data.Bytes()))
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		select {
		case out <- StreamEvent{Type: "error", Message: err.Error()}:
		case <-ctx.Done():
		}
	}
}

// GetTask fetches one task snapshot.
func (i *Invoker) GetTask(ctx context.Context, taskID string) (*Result, error) {
	method := "tasks/get"
	if i.version == ProtocolVersion10 {
		method = "GetTask"
	}
	body, _ := json.Marshal(map[string]any{"id": taskID})
	result, err := i.call(ctx, method, body)
	if err != nil {
		return nil, err
	}
	parsed := parseWireResult(result)
	if parsed == nil {
		return nil, fmt.Errorf("a2a invoker: unreadable task: %s", truncate(string(result), 512))
	}
	return parsed, nil
}

// Cancel cancels one task.
func (i *Invoker) Cancel(ctx context.Context, taskID string) error {
	method := "tasks/cancel"
	if i.version == ProtocolVersion10 {
		method = "CancelTask"
	}
	body, _ := json.Marshal(map[string]any{"id": taskID})
	_, err := i.call(ctx, method, body)
	return err
}

func (i *Invoker) methodSend() string {
	if i.version == ProtocolVersion10 {
		return "SendMessage"
	}
	return "message/send"
}

func (i *Invoker) methodStream() string {
	if i.version == ProtocolVersion10 {
		return "SendStreamingMessage"
	}
	return "message/stream"
}

func (i *Invoker) requestBody(req Request, stream bool) ([]byte, error) {
	text := req.Message
	if text == "" && req.TaskID == "" {
		return nil, errors.New("a2a invoker: message is required")
	}
	messageID := uuid.NewString()
	var message map[string]any
	var configuration map[string]any
	if i.version == ProtocolVersion10 {
		message = map[string]any{
			"messageId": messageID,
			"role":      "ROLE_USER",
			"parts":     []any{map[string]any{"text": text}},
		}
		configuration = map[string]any{
			"acceptedOutputModes": []string{"text/plain", "application/json"},
			"returnImmediately":   !stream,
		}
	} else {
		message = map[string]any{
			"messageId": messageID,
			"role":      "user",
			"parts":     []any{map[string]any{"kind": "text", "text": text}},
		}
		configuration = map[string]any{
			"acceptedOutputModes": []string{"text/plain", "application/json"},
			"blocking":            !stream,
		}
	}
	if req.ContextID != "" {
		message["contextId"] = req.ContextID
	}
	if req.TaskID != "" {
		message["taskId"] = req.TaskID
	}
	params := map[string]any{"message": message, "configuration": configuration}
	if req.Metadata != nil {
		params["metadata"] = req.Metadata
	}
	return json.Marshal(params)
}

func (i *Invoker) call(ctx context.Context, method string, params []byte) (json.RawMessage, error) {
	httpReq, err := i.newHTTPRequest(ctx, method, params)
	if err != nil {
		return nil, err
	}
	resp, err := i.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("a2a %s: status %d: %s", method, resp.StatusCode, truncate(string(body), 512))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    any    `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("a2a %s: decode response: %w", method, err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("a2a %s: code %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result, nil
}

func (i *Invoker) newHTTPRequest(ctx context.Context, method string, params []byte) (*http.Request, error) {
	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  json.RawMessage(params),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", i.version)
	for name, value := range i.headers {
		req.Header.Set(name, value)
	}
	return req, nil
}

// --- wire parsing ---

func parseWireResult(raw json.RawMessage) *Result {
	value := decodeObject(raw)
	if value == nil {
		return nil
	}
	// StreamResponse wrappers (1.0 streaming) carry the member by name.
	for _, key := range []string{"task", "message", "statusUpdate", "artifactUpdate"} {
		if inner, ok := value[key].(map[string]any); ok {
			value = inner
			break
		}
	}
	if isTask(value) {
		return resultFromTask(value)
	}
	if isMessage(value) {
		return resultFromMessage(value)
	}
	return nil
}

func parseWireEvent(raw []byte) (*StreamEvent, bool, error) {
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, false, fmt.Errorf("decode stream event: %w", err)
	}
	if envelope.Error != nil {
		return &StreamEvent{
			Type:    "error",
			Message: fmt.Sprintf("code %d: %s", envelope.Error.Code, envelope.Error.Message),
			Raw:     string(raw),
		}, true, nil
	}
	value := decodeObject(envelope.Result)
	if value == nil {
		return nil, false, nil
	}
	// 1.0 StreamResponse wrapper.
	for _, key := range []string{"task", "message", "statusUpdate", "artifactUpdate"} {
		if inner, ok := value[key].(map[string]any); ok {
			value = inner
			value["__wrapper"] = key
			break
		}
	}
	kind := stringAt(value, "kind")
	if kind == "" {
		if wrapper, ok := value["__wrapper"].(string); ok {
			kind = wrapper
		}
	}
	switch {
	case kind == "status-update" || kind == "statusUpdate" || value["status"] != nil && !isTaskPayload(value):
		state := normalizeState(stateOf(value))
		statusMessage := messageText(value["message"])
		if statusMessage == "" {
			statusMessage = messageText(objectAt(value, "status")["message"])
		}
		event := &StreamEvent{
			Type:      "status",
			State:     state,
			TaskID:    stringAt(value, "taskId"),
			ContextID: stringAt(value, "contextId"),
			Message:   statusMessage,
			Final:     boolAt(value, "final") || Terminal(state),
			Raw:       string(raw),
		}
		if event.TaskID == "" {
			event.TaskID = stringAt(value, "id")
		}
		if state == StateInputRequired || state == StateAuthRequired {
			event.Type = "interrupt"
		}
		return event, event.Final || event.Type == "interrupt", nil
	case kind == "artifact-update" || kind == "artifactUpdate":
		artifact := artifactFromMap(objectAt(value, "artifact"))
		if artifact == nil {
			return nil, false, nil
		}
		return &StreamEvent{
			Type:      "artifact",
			TaskID:    stringAt(value, "taskId"),
			ContextID: stringAt(value, "contextId"),
			Artifact:  artifact,
			Raw:       string(raw),
		}, false, nil
	case kind == "task" || isTaskPayload(value):
		result := resultFromTask(value)
		if result == nil {
			return nil, false, nil
		}
		return &StreamEvent{
			Type:      "status",
			State:     result.State,
			TaskID:    result.TaskID,
			ContextID: result.ContextID,
			// A peer that answers a stream with a whole task instead of deltas
			// puts the answer here. Without it a caller that only reads events
			// would see a completed task with no text. The artifacts travel
			// together with the text for the same reason.
			Text:      result.Text,
			Artifacts: result.Artifacts,
			Message:   result.Message,
			Final:     Terminal(result.State),
			Raw:       string(raw),
		}, Terminal(result.State), nil
	case kind == "message" || isMessage(value):
		text := partsText(value["parts"])
		return &StreamEvent{
			Type:  "delta",
			Text:  text,
			Final: false,
			Raw:   string(raw),
		}, false, nil
	default:
		return nil, false, nil
	}
}

func resultFromTask(value map[string]any) *Result {
	status := objectAt(value, "status")
	state := normalizeState(stateOf(value))
	if state == "" {
		state = StateUnknown
	}
	result := &Result{
		State:     state,
		TaskID:    stringAt(value, "id"),
		ContextID: stringAt(value, "contextId"),
		Message:   messageText(status["message"]),
		Artifacts: artifactsFromList(value["artifacts"]),
	}
	result.Text = artifactsText(result.Artifacts)
	if result.Text == "" {
		result.Text = messageText(status["message"])
	}
	if encoded, err := json.Marshal(value); err == nil {
		result.RawTask = string(encoded)
	}
	return result
}

func resultFromMessage(value map[string]any) *Result {
	text := partsText(value["parts"])
	return &Result{
		State:     StateCompleted,
		TaskID:    stringAt(value, "taskId"),
		ContextID: stringAt(value, "contextId"),
		Text:      text,
		Message:   text,
	}
}

func artifactsFromList(value any) []Artifact {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]Artifact, 0, len(list))
	for _, entry := range list {
		if item, ok := entry.(map[string]any); ok {
			if artifact := artifactFromMap(item); artifact != nil {
				out = append(out, *artifact)
			}
		}
	}
	return out
}

func artifactFromMap(value map[string]any) *Artifact {
	if value == nil {
		return nil
	}
	artifact := &Artifact{
		ID:          stringAt(value, "artifactId"),
		Name:        stringAt(value, "name"),
		Description: stringAt(value, "description"),
		Parts:       partsFromList(value["parts"]),
	}
	if artifact.ID == "" && len(artifact.Parts) == 0 {
		return nil
	}
	return artifact
}

func partsFromList(value any) []Part {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]Part, 0, len(list))
	for _, entry := range list {
		if item, ok := entry.(map[string]any); ok {
			out = append(out, partFromMap(item))
		}
	}
	return out
}

// partFromMap accepts both the 0.3 kind discriminator and the 1.0 flattened
// oneof (`text` / `data` / `url` / `raw`).
func partFromMap(value map[string]any) Part {
	part := Part{
		Kind:     stringAt(value, "kind"),
		FileName: stringAt(value, "filename"),
		MIMEType: stringAt(value, "mediaType"),
	}
	if part.Kind == "" {
		switch {
		case value["text"] != nil:
			part.Kind = "text"
		case value["data"] != nil:
			part.Kind = "data"
		case value["url"] != nil:
			part.Kind = "file"
			part.FileURL = stringAt(value, "url")
		case value["raw"] != nil:
			part.Kind = "file"
		}
	}
	switch part.Kind {
	case "text":
		part.Text = stringAt(value, "text")
	case "data":
		if data, err := json.Marshal(value["data"]); err == nil {
			part.Data = string(data)
		}
	case "file":
		if file, ok := value["file"].(map[string]any); ok {
			part.FileURL = stringAt(file, "uri")
			part.FileName = stringAt(file, "name")
			part.MIMEType = stringAt(file, "mimeType")
		}
		if part.FileURL == "" {
			part.FileURL = stringAt(value, "url")
		}
	}
	return part
}

func partsText(value any) string {
	parts := partsFromList(value)
	var builder strings.Builder
	for _, part := range parts {
		if part.Kind == "text" && part.Text != "" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func artifactsText(artifacts []Artifact) string {
	var builder strings.Builder
	for _, artifact := range artifacts {
		for _, part := range artifact.Parts {
			if part.Kind == "text" && part.Text != "" {
				builder.WriteString(part.Text)
			}
		}
	}
	return builder.String()
}

func messageText(value any) string {
	if message, ok := value.(map[string]any); ok {
		return partsText(message["parts"])
	}
	return ""
}

func stateOf(value map[string]any) string {
	if status, ok := value["status"].(map[string]any); ok {
		return stringAt(status, "state")
	}
	if state := stringAt(value, "state"); state != "" {
		return state
	}
	return ""
}

func normalizeState(state string) string {
	state = strings.TrimPrefix(state, "TASK_STATE_")
	state = strings.ToLower(state)
	return strings.ReplaceAll(state, "_", "-")
}

func isTask(value map[string]any) bool {
	return value["status"] != nil && (value["id"] != nil || value["contextId"] != nil)
}

func isTaskPayload(value map[string]any) bool {
	return value["status"] != nil && value["artifacts"] != nil
}

func isMessage(value map[string]any) bool {
	_, hasParts := value["parts"]
	_, hasRole := value["role"]
	return hasParts && hasRole
}

func decodeObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

func objectAt(value map[string]any, key string) map[string]any {
	if value == nil {
		return nil
	}
	object, _ := value[key].(map[string]any)
	return object
}
