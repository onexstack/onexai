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

// Package a2aclient owns the wire shapes of an external A2A agent: the
// AgentCard, its version normalization, and the HTTP client used to fetch it.
//
// The package deliberately does not depend on either A2A SDK. The 0.3 and 1.0
// modules have incompatible Go types, and leaking them into the controller,
// the API or the console would make every version difference everyone's
// problem. The SDKs are used behind Session implementations; this file is the
// neutral card DTO both of them project onto.
package a2aclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// WellKnownCardPath is the A2A 1.0/0.3 discovery path.
	WellKnownCardPath = "/.well-known/agent-card.json"
	// LegacyWellKnownCardPath is the 0.2 discovery path.
	LegacyWellKnownCardPath = "/.well-known/agent.json"

	ProtocolVersion03 = "0.3"
	ProtocolVersion10 = "1.0"

	TransportJSONRPC  = "JSONRPC"
	TransportHTTPJSON = "HTTP+JSON"
	TransportGRPC     = "GRPC"
)

// Interface is one invocation endpoint declared by a card.
type Interface struct {
	URL             string `json:"url"`
	Transport       string `json:"transport"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	Tenant          string `json:"tenant,omitempty"`
}

// Skill is one card skill.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// Capabilities is the optional capability declaration.
type Capabilities struct {
	Streaming              bool `json:"streaming,omitempty"`
	PushNotifications      bool `json:"pushNotifications,omitempty"`
	ExtendedAgentCard      bool `json:"extendedAgentCard,omitempty"`
	StateTransitionHistory bool `json:"stateTransitionHistory,omitempty"`
}

// Extension is one protocol extension.
type Extension struct {
	URI         string `json:"uri"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Card is the normalized AgentCard the platform stores and renders.
type Card struct {
	ProtocolVersion  string          `json:"protocolVersion,omitempty"`
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	Version          string          `json:"version,omitempty"`
	Provider         string          `json:"provider,omitempty"`
	DocumentationURL string          `json:"documentationUrl,omitempty"`
	IconURL          string          `json:"iconUrl,omitempty"`
	Interfaces       []Interface     `json:"interfaces,omitempty"`
	Capabilities     Capabilities    `json:"capabilities,omitempty"`
	InputModes       []string        `json:"inputModes,omitempty"`
	OutputModes      []string        `json:"outputModes,omitempty"`
	Skills           []Skill         `json:"skills,omitempty"`
	SecuritySchemes  json.RawMessage `json:"securitySchemes,omitempty"`
	Extensions       []Extension     `json:"extensions,omitempty"`

	// Raw is the last good card as fetched. It is kept so the console can show
	// the original JSON, and so a digest change is explainable.
	Raw       json.RawMessage `json:"raw,omitempty"`
	Digest    string          `json:"digest,omitempty"`
	SourceURL string          `json:"sourceUrl,omitempty"`
}

// Transport returns the first usable transport, preferring JSON-RPC.
func (c *Card) Transport() string {
	for _, iface := range c.Interfaces {
		if iface.Transport == TransportJSONRPC {
			return TransportJSONRPC
		}
	}
	for _, iface := range c.Interfaces {
		if iface.Transport == TransportHTTPJSON {
			return TransportHTTPJSON
		}
	}
	return ""
}

// ResolveRelative turns relative interface URLs into absolute ones against the
// card's own location. onexai publishes a path-relative interface when no
// public URL is configured; a client that kept it relative would then POST to
// its own origin.
func (c *Card) ResolveRelative(sourceURL string) {
	if c == nil || sourceURL == "" {
		return
	}
	base, err := url.Parse(sourceURL)
	if err != nil {
		return
	}
	for i := range c.Interfaces {
		if c.Interfaces[i].URL == "" {
			continue
		}
		u, err := url.Parse(c.Interfaces[i].URL)
		if err != nil || u.IsAbs() {
			continue
		}
		c.Interfaces[i].URL = base.ResolveReference(u).String()
	}
}

// Endpoint returns the first invocation URL the platform can dial.
func (c *Card) Endpoint() string {
	for _, iface := range c.Interfaces {
		if iface.URL != "" && iface.Transport != TransportGRPC {
			return iface.URL
		}
	}
	return ""
}

// ParseCard normalizes a 0.3 or 1.0 AgentCard.
//
// Detection is shape-first: a card with supportedInterfaces is 1.0, a card with
// url/preferredTransport is 0.3. protocolVersion is only the fallback, because
// the field moved between the two versions and is not always populated.
func ParseCard(raw []byte) (*Card, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode agent card: %w", err)
	}
	if len(root) == 0 {
		return nil, errors.New("agent card is empty")
	}

	card := &Card{
		Name:             stringAt(root, "name"),
		Description:      stringAt(root, "description"),
		Version:          stringAt(root, "version"),
		DocumentationURL: stringAt(root, "documentationUrl"),
		IconURL:          stringAt(root, "iconUrl"),
		Raw:              append(json.RawMessage(nil), raw...),
	}
	card.ProtocolVersion = detectVersion(root)
	if provider, ok := root["provider"].(map[string]any); ok {
		card.Provider = stringAt(provider, "organization")
	}
	if card.Name == "" {
		return nil, errors.New("agent card has no name")
	}

	// Interfaces: 1.0 supportedInterfaces, then 0.3 url/preferredTransport and
	// additionalInterfaces. Both are normalized into the same list.
	if interfaces, ok := root["supportedInterfaces"].([]any); ok {
		for _, entry := range interfaces {
			if item, ok := entry.(map[string]any); ok {
				card.Interfaces = append(card.Interfaces, Interface{
					URL:             stringAt(item, "url"),
					Transport:       normalizeTransport(stringAt(item, "protocolBinding")),
					ProtocolVersion: stringAt(item, "protocolVersion"),
					Tenant:          stringAt(item, "tenant"),
				})
			}
		}
	}
	if len(card.Interfaces) == 0 {
		preferred := normalizeTransport(stringAt(root, "preferredTransport"))
		if preferred == "" {
			preferred = TransportJSONRPC
		}
		if endpoint := stringAt(root, "url"); endpoint != "" {
			card.Interfaces = append(card.Interfaces, Interface{URL: endpoint, Transport: preferred})
		}
	}
	if interfaces, ok := root["additionalInterfaces"].([]any); ok {
		for _, entry := range interfaces {
			if item, ok := entry.(map[string]any); ok {
				card.Interfaces = append(card.Interfaces, Interface{
					URL:       stringAt(item, "url"),
					Transport: normalizeTransport(stringAt(item, "transport")),
				})
			}
		}
	}
	if len(card.Interfaces) == 0 {
		return nil, errors.New("agent card declares no interface")
	}

	// Capabilities. 1.0 uses extendedAgentCard; 0.3 uses
	// supportsAuthenticatedExtendedCard.
	if caps, ok := root["capabilities"].(map[string]any); ok {
		card.Capabilities.Streaming = boolAt(caps, "streaming")
		card.Capabilities.PushNotifications = boolAt(caps, "pushNotifications")
		card.Capabilities.ExtendedAgentCard = boolAt(caps, "extendedAgentCard")
		card.Capabilities.StateTransitionHistory = boolAt(caps, "stateTransitionHistory")
		if extensions, ok := caps["extensions"].([]any); ok {
			for _, entry := range extensions {
				if item, ok := entry.(map[string]any); ok {
					card.Extensions = append(card.Extensions, Extension{
						URI:         stringAt(item, "uri"),
						Description: stringAt(item, "description"),
						Required:    boolAt(item, "required"),
					})
				}
			}
		}
	}
	if boolAt(root, "supportsAuthenticatedExtendedCard") || boolAt(root, "extendedAgentCard") {
		card.Capabilities.ExtendedAgentCard = true
	}

	card.InputModes = stringSlice(root["defaultInputModes"])
	card.OutputModes = stringSlice(root["defaultOutputModes"])
	if skills, ok := root["skills"].([]any); ok {
		for _, entry := range skills {
			if item, ok := entry.(map[string]any); ok {
				card.Skills = append(card.Skills, Skill{
					ID:          stringAt(item, "id"),
					Name:        stringAt(item, "name"),
					Description: stringAt(item, "description"),
					Tags:        stringSlice(item["tags"]),
					Examples:    stringSlice(item["examples"]),
					InputModes:  stringSlice(item["inputModes"]),
					OutputModes: stringSlice(item["outputModes"]),
				})
			}
		}
	}
	if schemes, ok := root["securitySchemes"]; ok {
		card.SecuritySchemes, _ = json.Marshal(schemes)
	}
	card.Digest = digestJSON(root)
	return card, nil
}

// CandidateCardURLs is the discovery order for a card.
//
// A deployment may publish the card at the origin root while its transport
// lives under a path (onexai itself does exactly this), so origin-root
// discovery comes before endpoint-relative discovery.
func CandidateCardURLs(endpoint, explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	trimmed := strings.TrimSuffix(endpoint, "/")
	return []string{
		origin + WellKnownCardPath,
		origin + LegacyWellKnownCardPath,
		trimmed + WellKnownCardPath,
	}
}

// FetchCard tries the candidates in order and returns the first card that
// parses. Transport errors and non-200 statuses are collected so the last
// meaningful failure can be shown to the user.
func FetchCard(ctx context.Context, client *http.Client, candidates []string, headers map[string]string) (*Card, error) {
	if client == nil {
		return nil, errors.New("a2a client: http client is required")
	}
	if len(candidates) == 0 {
		return nil, errors.New("a2a client: no card URL candidates")
	}
	var failures []string
	for _, candidate := range candidates {
		card, err := fetchOne(ctx, client, candidate, headers)
		if err == nil {
			return card, nil
		}
		failures = append(failures, candidate+": "+err.Error())
	}
	return nil, fmt.Errorf("fetch agent card failed: %s", strings.Join(failures, "; "))
}

func fetchOne(ctx context.Context, client *http.Client, target string, headers map[string]string) (*Card, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	card, err := ParseCard(body)
	if err != nil {
		return nil, err
	}
	card.SourceURL = target
	card.ResolveRelative(target)
	return card, nil
}

func detectVersion(root map[string]any) string {
	if _, ok := root["supportedInterfaces"]; ok {
		return ProtocolVersion10
	}
	if _, ok := root["url"]; ok {
		return ProtocolVersion03
	}
	if _, ok := root["preferredTransport"]; ok {
		return ProtocolVersion03
	}
	version := stringAt(root, "protocolVersion")
	switch {
	case strings.HasPrefix(version, "1."):
		return ProtocolVersion10
	case strings.HasPrefix(version, "0."):
		return ProtocolVersion03
	default:
		return ProtocolVersion03
	}
}

func normalizeTransport(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "JSONRPC", "JSON-RPC", "JSON_RPC":
		return TransportJSONRPC
	case "HTTP+JSON", "HTTP_JSON", "HTTPJSON", "REST":
		return TransportHTTPJSON
	case "GRPC":
		return TransportGRPC
	default:
		return value
	}
}

func digestJSON(root map[string]any) string {
	// json.Marshal sorts map keys, so a remote that reorders fields does not
	// change the digest.
	canonical, err := json.Marshal(root)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func stringAt(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolAt(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func stringSlice(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

// ClientWithHeaders wraps an existing HTTP client's transport with static
// headers. The underlying client is expected to carry the netguard transport,
// so the request still goes through the SSRF checks.
func ClientWithHeaders(base *http.Client, headers map[string]string) *http.Client {
	if base == nil || len(headers) == 0 {
		return base
	}
	clone := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = &headerTransport{base: transport, headers: headers}
	return &clone
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for name, value := range t.headers {
		clone.Header.Set(name, value)
	}
	return t.base.RoundTrip(clone)
}

// TimeoutClient bounds one card fetch. netguard's fetcher already bounds the
// transport; this is the outer deadline the controller can rely on.
func TimeoutClient(base *http.Client, timeout time.Duration) *http.Client {
	if base == nil || timeout <= 0 {
		return base
	}
	clone := *base
	clone.Timeout = timeout
	return &clone
}
