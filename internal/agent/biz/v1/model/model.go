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

// Package model implements the model-endpoint business logic.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/onexstack/onexstack/pkg/store/where"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/agent/pkg/llmhttp"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/netguard"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// Config carries the model-endpoint settings: how a key is encrypted at rest and
// how a connectivity test is allowed to reach the network.
type Config struct {
	// EncryptionKey seals an API key at rest. Empty means a user-owned endpoint
	// cannot be saved: its key is never written in plaintext.
	EncryptionKey string
	// AllowPrivateEndpoints lets a user endpoint reach private/loopback
	// addresses. See options.LLMOptions.
	AllowPrivateEndpoints bool
	// TestTimeout bounds one connectivity test.
	TestTimeout time.Duration
}

// ModelBiz is the model-endpoint business logic.
type ModelBiz interface {
	Create(ctx context.Context, req *v1.CreateModelRequest) (*v1.CreateModelResponse, error)
	Get(ctx context.Context, req *v1.GetModelRequest) (*v1.GetModelResponse, error)
	List(ctx context.Context, req *v1.ListModelRequest) (*v1.ListModelResponse, error)
	Update(ctx context.Context, req *v1.UpdateModelRequest) (*v1.UpdateModelResponse, error)
	Delete(ctx context.Context, req *v1.DeleteModelRequest) (*v1.DeleteModelResponse, error)
	Test(ctx context.Context, req *v1.TestModelRequest) (*v1.TestModelResponse, error)
	// Reveal returns the plaintext upstream key of an endpoint the caller owns,
	// or of any endpoint for an administrator. An empty string is a real answer:
	// the row has no key, or this deployment cannot decrypt the stored one.
	Reveal(ctx context.Context, name string, caller Caller) (string, error)
}

// Caller is who is asking for a plaintext key. It mirrors handler.isOwner's
// rule: a row's owner column holds a user ID, and historical rows written before
// that rule may hold the display name instead.
type Caller struct {
	UserID  string
	Name    string
	IsAdmin bool
}

// owns reports whether the caller is the row's owner. A platform row has no
// owner, so it is administrator-only.
func (c Caller) owns(owner string) bool {
	if owner == "" {
		return false
	}
	return owner == c.UserID || (c.Name != "" && owner == c.Name)
}

type modelBiz struct {
	store  agentstore.AIStore
	config *Config
}

var _ ModelBiz = (*modelBiz)(nil)

// New creates a ModelBiz.
func New(store agentstore.AIStore, config *Config) *modelBiz {
	if config == nil {
		config = &Config{}
	}
	return &modelBiz{store: store, config: config}
}

// Create implements ModelBiz.
func (b *modelBiz) Create(ctx context.Context, req *v1.CreateModelRequest) (*v1.CreateModelResponse, error) {
	row := conversion.ModelV1ToModelModel(req.GetModel())
	if err := resolveBaseURL(row); err != nil {
		return nil, err
	}
	// A user-owned endpoint must carry a key, and that key is never stored in
	// plaintext. A platform endpoint may still be written without one while a
	// deployment has no encryption key configured, which is the legacy shape.
	if row.Owner != "" && b.config.EncryptionKey == "" {
		return nil, errno.ErrInternal.WithMessage(
			"model api-key encryption is not configured; a user-owned endpoint cannot be saved")
	}
	if agentmodel.IsSealedAPIKey(row.APIKey) {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"api key must be plaintext; the enc:v1: form is reserved for storage")
	}
	// The response carries the key the caller just submitted, once. The row
	// holds the sealed form, which is not a usable credential, so it must never
	// be echoed back as if it were the key.
	plainKey := row.APIKey
	sealed, err := b.sealKey(plainKey)
	if err != nil {
		return nil, err
	}
	row.APIKey = sealed
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.Models().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	model := conversion.ModelModelToModelV1(row)
	model.ApiKey = plainKey
	return &v1.CreateModelResponse{Model: model}, nil
}

// Get implements ModelBiz.
func (b *modelBiz) Get(ctx context.Context, req *v1.GetModelRequest) (*v1.GetModelResponse, error) {
	row, err := b.store.Models().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "model %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetModelResponse{Model: conversion.ModelModelToModelV1(row)}, nil
}

// Reveal implements ModelBiz.
//
// # Why this is a separate read
//
// Get and List never carry the key: a model row holds an upstream provider's
// credential that bills, and reads of the catalog are open to every signed-in
// user. A page or a list load must not put the key in the response. The console
// fetches it only when the owner presses "查看", and the plaintext lives in that
// tab's memory rather than in a cache.
//
// # Who may read it
//
// The same rule as writing the row: its owner or an administrator. A platform
// endpoint has no owner, so only an administrator can reveal it. An empty result
// is a real answer — there is no stored key, or the deployment cannot decrypt
// it — and the caller renders it as "不可查看" rather than as an error.
func (b *modelBiz) Reveal(ctx context.Context, name string, caller Caller) (string, error) {
	if b.store == nil {
		return "", errno.ErrNotConfigured.WithMessage("服务未配置资源存储，无法读取模型密钥")
	}
	row, err := b.store.Models().Get(ctx, where.F("name", name))
	if err := storeerr.GetError(row, err, "model %q not found", name); err != nil {
		return "", err
	}
	if !caller.IsAdmin && !caller.owns(row.Owner) {
		return "", errno.ErrPermissionDenied.WithMessage("只有资源归属人或管理员可以查看该模型密钥")
	}
	if row.APIKey == "" {
		return "", nil
	}
	key, err := b.openKey(row.APIKey)
	if err != nil {
		// A key sealed with a different deployment key is not a server failure
		// from the caller's point of view: it still works, it just cannot be
		// displayed. The AK/SK reveal answers the same way.
		return "", nil //nolint:nilerr
	}
	return key, nil
}

// List implements ModelBiz.
func (b *modelBiz) List(ctx context.Context, req *v1.ListModelRequest) (*v1.ListModelResponse, error) {
	total, list, err := b.store.Models().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if authzview.ReadFiltered(ctx, "model") {
		before := len(list)
		list = authzview.Filter(ctx, "model", list, func(row *agentmodel.ModelM) (string, string, string) {
			return "", row.Name, row.Owner
		})
		total = authzview.AdjustCount(total, before-len(list))
	}
	models := make([]*v1.Model, 0, len(list))
	for _, row := range list {
		models = append(models, conversion.ModelModelToModelV1(row))
	}
	return &v1.ListModelResponse{Models: models, Count: total}, nil
}

// Update implements ModelBiz.
func (b *modelBiz) Update(ctx context.Context, req *v1.UpdateModelRequest) (*v1.UpdateModelResponse, error) {
	row := conversion.ModelV1ToModelModel(req.GetModel())
	// Check the submitted value before mergeModelUpdate fills an omitted key
	// from storage: that filled value is the sealed form and must pass through
	// SealAPIKey unchanged. Only a caller-supplied ciphertext is rejected.
	if agentmodel.IsSealedAPIKey(row.APIKey) {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"api key must be plaintext; the enc:v1: form is reserved for storage")
	}
	existing, err := b.store.Models().Get(ctx, where.F("name", row.Name))
	if err := storeerr.GetError(existing, err, "model %q not found", row.Name); err != nil {
		return nil, err
	}

	mergeModelUpdate(row, existing)
	if err := resolveBaseURL(row); err != nil {
		return nil, err
	}
	row.UpdatedAt = time.Now().UnixMilli()

	sealed, err := b.sealKey(row.APIKey)
	if err != nil {
		return nil, err
	}
	row.APIKey = sealed

	if err := b.store.Models().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdateModelResponse{Model: conversion.ModelModelToModelV1(row)}, nil
}

// mergeModelUpdate fills the stored values a full-row update would otherwise
// erase. The stored key is already sealed; SealAPIKey passes it through.
func mergeModelUpdate(row, existing *agentmodel.ModelM) {
	if row.APIKey == "" {
		row.APIKey = existing.APIKey
	}
	if row.Owner == "" {
		row.Owner = existing.Owner
	}
	if len(row.Capabilities) == 0 {
		row.Capabilities = existing.Capabilities
	}
	if row.Provider == "" {
		row.Provider = existing.Provider
	}
	if row.Model == "" {
		row.Model = existing.Model
	}
	if row.BaseURL == "" {
		row.BaseURL = existing.BaseURL
	}
	if row.CreatedAt == 0 {
		row.CreatedAt = existing.CreatedAt
	}
}

// resolveBaseURL fills an endpoint the caller left empty from the provider's
// default, inferring the provider from the model name when neither is set.
//
// # Why this cannot be left to the runtime fallback
//
// The control-plane Model object declares spec.endpoint required, so a blank
// endpoint is rejected when the object is written — before any reader gets the
// chance to apply its fallback. That surfaced as "A database write operation
// failed." with no hint about the actual cause. Resolving the default here keeps
// the stored object self-describing, and turns "this provider has no default"
// into a clear 400 instead of an opaque write failure.
func resolveBaseURL(row *agentmodel.ModelM) error {
	if row.BaseURL != "" {
		return nil
	}
	provider := row.Provider
	if provider == "" {
		provider = chatmodel.InferProvider(row.Model)
	}
	baseURL := chatmodel.DefaultBaseURL(provider)
	if baseURL == "" {
		return errno.ErrInvalidArgument.WithMessage(
			"model %q requires a base URL: provider %q has no default", row.Name, provider)
	}
	row.BaseURL = baseURL
	return nil
}

// Delete implements ModelBiz.
func (b *modelBiz) Delete(ctx context.Context, req *v1.DeleteModelRequest) (*v1.DeleteModelResponse, error) {
	if err := b.store.Models().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteModelResponse{}, nil
}

// Test implements ModelBiz: one minimal completion against an endpoint, without
// persisting anything.
//
// The response is 200 with ok=false for a failed test: the request was valid,
// the endpoint was not. That is a different answer from a bind or storage
// failure, and the caller can show it without treating the API as broken.
func (b *modelBiz) Test(ctx context.Context, req *v1.TestModelRequest) (*v1.TestModelResponse, error) {
	// Default filled the inline model and name. The stored row supplies the
	// provider/base URL/model the caller omitted, because only this layer may
	// read it; the validator sees the request as sent. A named row that cannot
	// be read is an API error (404 for absent, 500 for an outage), not a failed
	// probe: the test never ran.
	inline := req.GetModel()
	if err := b.fillTestModelFromStored(ctx, req.GetName(), inline); err != nil {
		return nil, err
	}
	if inline.GetBaseUrl() == "" {
		inline.BaseUrl = chatmodel.DefaultBaseURL(inline.GetProvider())
	}

	key, err := b.testKey(ctx, req.GetName(), inline.GetApiKey())
	if err != nil {
		return nil, err
	}

	baseURL := inline.GetBaseUrl()
	if baseURL == "" || inline.GetModel() == "" || key == "" {
		return &v1.TestModelResponse{
			Ok:      false,
			Message: "configuration is incomplete (base URL, model and api key are required)",
		}, nil
	}

	timeout, allowPrivate := b.probeSettings()

	return probeModel(ctx, baseURL, key, inline.GetModel(), timeout, allowPrivate), nil
}

// fillTestModelFromStored fills the endpoint fields a connectivity test can
// derive from the stored row. A named row that does not exist is a 404; an
// outage is a 500. Either way the test never ran, so answering ok=false would
// claim an endpoint problem that is not there.
func (b *modelBiz) fillTestModelFromStored(ctx context.Context, name string, m *v1.Model) error {
	if name == "" || b.store == nil {
		return nil
	}

	existing, err := b.store.Models().Get(ctx, where.F("name", name))
	if err := storeerr.GetError(existing, err, "model %q not found", name); err != nil {
		return err
	}

	if m.GetProvider() == "" {
		m.Provider = existing.Provider //nolint:protogetter // writing the request field
	}
	if m.GetBaseUrl() == "" {
		m.BaseUrl = existing.BaseURL //nolint:protogetter // writing the request field
	}
	if m.GetModel() == "" {
		m.Model = existing.Model //nolint:protogetter // writing the request field
	}

	return nil
}

// testKey resolves the key to probe with. A key supplied inline wins; an empty
// one is read back from the stored endpoint and decrypted. An unreadable row is
// an API error, not a probe result.
func (b *modelBiz) testKey(ctx context.Context, name, inline string) (string, error) {
	if inline != "" || name == "" || b.store == nil {
		return inline, nil
	}

	row, err := b.store.Models().Get(ctx, where.F("name", name))
	if err := storeerr.GetError(row, err, "model %q not found", name); err != nil {
		return "", err
	}

	return b.openKey(row.APIKey)
}

// probeSettings returns the deployment's probe timeout and SSRF policy.
func (b *modelBiz) probeSettings() (time.Duration, bool) {
	timeout := 15 * time.Second
	allowPrivate := false
	if b.config.TestTimeout > 0 {
		timeout = b.config.TestTimeout
	}
	allowPrivate = b.config.AllowPrivateEndpoints
	return timeout, allowPrivate
}

// probeModel performs the one minimal completion the test endpoint promises. A
// failure is a 200 with ok=false: the request was valid, the endpoint was not.
func probeModel(
	ctx context.Context, baseURL, key, model string, timeout time.Duration, allowPrivate bool,
) *v1.TestModelResponse {
	client := netguard.NewFetcher(netguard.Options{
		AllowPrivateHosts: allowPrivate,
		Timeout:           timeout,
	}).Client()
	cm := chatmodel.NewOpenAIChatModel(baseURL, key, model).WithHTTPClient(client)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	_, err := cm.Generate(runCtx, []*schema.Message{{Role: schema.User, Content: "ping"}})
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return &v1.TestModelResponse{
			Ok:        false,
			LatencyMs: latency,
			Message:   describeProbeError(err, key, timeout),
		}
	}

	return &v1.TestModelResponse{Ok: true, LatencyMs: latency, Message: "ok"}
}

// maxProbeMessageRunes bounds the failure description a caller renders. The
// upstream body is provider-controlled; keeping one line makes the message
// readable in a notification and keeps an oversized error from being echoed.
const maxProbeMessageRunes = 300

// describeProbeError turns a probe failure into one actionable line.
//
// An upstream HTTP failure keeps its status code and the provider's own
// explanation, which is what tells a user whether the model name, the key or
// the URL is wrong. Transport failures keep a bounded description with the
// request URL removed; the URL is the user's own input and may carry
// credentials in a query string.
func describeProbeError(err error, apiKey string, timeout time.Duration) string {
	var httpErr *llmhttp.HTTPError
	if errors.As(err, &httpErr) {
		msg := fmt.Sprintf("upstream returned %d", httpErr.Code)
		if summary := summarizeUpstreamBody(httpErr.Body); summary != "" {
			msg += ": " + summary
		}
		// A bare 404 is usually a base URL that speaks another protocol
		// (Anthropic's /anthropic path, for example) or a model name the
		// endpoint does not serve. Neither is obvious from the status alone.
		if httpErr.Code == http.StatusNotFound {
			msg += " (check that the base URL is an OpenAI-compatible endpoint and the model name exists there)"
		}
		return redactSecret(msg, apiKey)
	}

	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		if timeout > 0 {
			return fmt.Sprintf("connection timed out after %s", timeout)
		}
		return "connection timed out"
	}

	// netguard's refusal names the internal address it resolved. The policy is
	// the useful fact, so the address stays out of the response.
	if errors.Is(err, netguard.ErrBlockedTarget) {
		return "connection failed: endpoint address is blocked (private or loopback targets are not allowed)"
	}

	return redactSecret("connection failed: "+sanitizeTransportError(err), apiKey)
}

// isTimeout covers http.Client's own deadline, which is a net.Error even when
// errors.Is(err, context.DeadlineExceeded) is false.
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// sanitizeTransportError drops the *url.Error wrapper: its URL can carry
// credentials in the query string, and the dial error alone is what a user
// needs to act on.
func sanitizeTransportError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	return collapseAndTruncate(err.Error())
}

// summarizeUpstreamBody prefers the provider's own explanation. OpenAI and
// Anthropic both use {"error":{"message":"..."}}; some gateways use a bare
// string error or a top-level message. Anything else falls back to the body
// itself, collapsed and truncated.
func summarizeUpstreamBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}

	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal([]byte(trimmed), &envelope) == nil {
		if len(envelope.Error) > 0 {
			var obj struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(envelope.Error, &obj) == nil && strings.TrimSpace(obj.Message) != "" {
				return collapseAndTruncate(obj.Message)
			}
			var text string
			if json.Unmarshal(envelope.Error, &text) == nil && strings.TrimSpace(text) != "" {
				return collapseAndTruncate(text)
			}
		}
		if strings.TrimSpace(envelope.Message) != "" {
			return collapseAndTruncate(envelope.Message)
		}
	}

	return collapseAndTruncate(trimmed)
}

// collapseAndTruncate turns a multi-line provider payload into one bounded
// line that fits a notification.
func collapseAndTruncate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxProbeMessageRunes {
		return s
	}
	return string(r[:maxProbeMessageRunes]) + "…"
}

// redactSecret keeps a provider that echoes the credential out of the response.
func redactSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[redacted]")
}

// sealKey encrypts a key for storage. A deployment without an encryption key
// keeps the legacy plaintext behaviour for platform-owned rows; a user-owned row
// cannot reach here without a key (Create refuses it).
func (b *modelBiz) sealKey(key string) (string, error) {
	if key == "" {
		return "", nil
	}
	sealed, err := agentmodel.SealAPIKey(key, b.config.EncryptionKey)
	if err != nil {
		return "", errno.ErrInternal.WithMessage("the model api key could not be sealed")
	}
	return sealed, nil
}

// openKey decrypts a stored key. A legacy plaintext value passes through.
func (b *modelBiz) openKey(stored string) (string, error) {
	opened, err := agentmodel.OpenAPIKey(stored, b.config.EncryptionKey)
	if err != nil {
		return "", errno.ErrInternal.WithMessage("the model api key could not be opened")
	}
	return opened, nil
}
