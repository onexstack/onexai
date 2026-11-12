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

package model

import (
	"encoding/json"
	"fmt"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&CustomCommandM{}) }

// CustomCommandM is a user-defined slash command. Its nested specs are carried
// as JSON text, the same way SkillM carries provider params: the columns are a
// row shape, not a query surface.
type CustomCommandM struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name           string     `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName    string     `gorm:"size:256" json:"display_name"`
	Description    string     `gorm:"type:text" json:"description"`
	Status         string     `gorm:"size:32" json:"status"`
	Type           string     `gorm:"column:type;size:32" json:"type"`
	Aliases        StringList `gorm:"type:text" json:"aliases"`
	Usage          string     `gorm:"type:text" json:"usage"`
	Parameters     string     `gorm:"type:text" json:"parameters"`
	Prompt         string     `gorm:"type:text" json:"prompt"`
	HTTP           string     `gorm:"type:text" json:"http"`
	Tool           string     `gorm:"type:text" json:"tool"`
	Output         string     `gorm:"type:text" json:"output"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	Namespace      string     `gorm:"column:namespace;size:128;index" json:"namespace"`
	Labels         StringMap  `gorm:"-" json:"labels"`
	CreatedAt      int64      `json:"created_at"`
	UpdatedAt      int64      `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the custom command table name.
func (CustomCommandM) TableName() string { return dbschema.Table(dbschema.Agent, "custom_command") }

// Enabled reports whether the command takes part in resolution.
func (c CustomCommandM) Enabled() bool { return c.Status != StatusDisabled }

// GetNamespace returns the normalized namespace.
func (c CustomCommandM) GetNamespace() string { return NormalizeNamespace(c.Namespace) }

// CustomCommandSpecM is the decoded form of a custom command's spec. The JSON
// tags match the versioned API type so the control-plane conversion can marshal
// one straight into the row's text columns.
type CustomCommandSpecM struct {
	Type           string              `json:"type"`
	Aliases        []string            `json:"aliases,omitempty"`
	Usage          string              `json:"usage,omitempty"`
	Parameters     []CommandParameterM `json:"parameters,omitempty"`
	Prompt         *PromptCommandM     `json:"prompt,omitempty"`
	HTTP           *HTTPCommandM       `json:"http,omitempty"`
	Tool           *ToolCommandM       `json:"tool,omitempty"`
	Output         *CommandOutputM     `json:"output,omitempty"`
	TimeoutSeconds int32               `json:"timeoutSeconds,omitempty"`
}

// CommandParameterM is one strictly-typed argument.
type CommandParameterM struct {
	Name        string          `json:"name"`
	In          string          `json:"in,omitempty"`
	Type        string          `json:"type,omitempty"`
	Description string          `json:"description,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	Enum        []string        `json:"enum,omitempty"`
	Format      string          `json:"format,omitempty"`
	Short       string          `json:"short,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// PromptCommandM renders a template and hands the result to the agent.
type PromptCommandM struct {
	Template string `json:"template"`
}

// ServiceReferenceM points at an in-cluster Service.
type ServiceReferenceM struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int32  `json:"port,omitempty"`
	Path      string `json:"path,omitempty"`
}

// HTTPHeaderM is one outbound header.
type HTTPHeaderM struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ReferenceM names another object in the group.
type ReferenceM struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// HTTPCommandM proxies an external REST endpoint.
type HTTPCommandM struct {
	Method                string             `json:"method,omitempty"`
	URL                   string             `json:"url,omitempty"`
	Service               *ServiceReferenceM `json:"service,omitempty"`
	CABundle              string             `json:"caBundle,omitempty"`
	InsecureSkipTLSVerify bool               `json:"insecureSkipTLSVerify,omitempty"`
	ClientConfig          json.RawMessage    `json:"clientConfig,omitempty"`
	Headers               []HTTPHeaderM      `json:"headers,omitempty"`
	Query                 map[string]string  `json:"query,omitempty"`
	Body                  string             `json:"body,omitempty"`
	ContentType           string             `json:"contentType,omitempty"`
	SecretRef             *ReferenceM        `json:"secretRef,omitempty"`
	SideEffects           string             `json:"sideEffects,omitempty"`
}

// ToolCommandM calls one tool on an MCP server.
type ToolCommandM struct {
	MCPServer         ReferenceM `json:"mcpServer"`
	Tool              string     `json:"tool"`
	ArgumentsTemplate string     `json:"argumentsTemplate,omitempty"`
}

// CommandOutputM renders the result of an http or tool command.
type CommandOutputM struct {
	Format   string          `json:"format,omitempty"`
	Template string          `json:"template,omitempty"`
	Schema   json.RawMessage `json:"schema,omitempty"`
}

// Spec decodes the row's text columns into the typed spec.
func (c *CustomCommandM) Spec() (*CustomCommandSpecM, error) {
	spec := &CustomCommandSpecM{
		Type:           c.Type,
		Aliases:        append([]string(nil), c.Aliases...),
		Usage:          c.Usage,
		TimeoutSeconds: int32(c.TimeoutSeconds),
	}
	if err := decodeJSONColumn(c.Parameters, &spec.Parameters); err != nil {
		return nil, fmt.Errorf("decoding parameters of custom command %q: %w", c.Name, err)
	}
	if err := decodeJSONPtr(c.Prompt, &spec.Prompt); err != nil {
		return nil, fmt.Errorf("decoding prompt of custom command %q: %w", c.Name, err)
	}
	if err := decodeJSONPtr(c.HTTP, &spec.HTTP); err != nil {
		return nil, fmt.Errorf("decoding http of custom command %q: %w", c.Name, err)
	}
	if err := decodeJSONPtr(c.Tool, &spec.Tool); err != nil {
		return nil, fmt.Errorf("decoding tool of custom command %q: %w", c.Name, err)
	}
	if err := decodeJSONPtr(c.Output, &spec.Output); err != nil {
		return nil, fmt.Errorf("decoding output of custom command %q: %w", c.Name, err)
	}
	return spec, nil
}

// SetSpec encodes the typed spec into the row's text columns.
func (c *CustomCommandM) SetSpec(spec *CustomCommandSpecM) error {
	c.Type = spec.Type
	c.Aliases = append(StringList(nil), spec.Aliases...)
	c.Usage = spec.Usage
	c.TimeoutSeconds = int(spec.TimeoutSeconds)

	var err error
	if c.Parameters, err = encodeJSONColumn(spec.Parameters); err != nil {
		return fmt.Errorf("encoding parameters of custom command %q: %w", c.Name, err)
	}
	if c.Prompt, err = encodeJSONPtr(spec.Prompt); err != nil {
		return fmt.Errorf("encoding prompt of custom command %q: %w", c.Name, err)
	}
	if c.HTTP, err = encodeJSONPtr(spec.HTTP); err != nil {
		return fmt.Errorf("encoding http of custom command %q: %w", c.Name, err)
	}
	if c.Tool, err = encodeJSONPtr(spec.Tool); err != nil {
		return fmt.Errorf("encoding tool of custom command %q: %w", c.Name, err)
	}
	if c.Output, err = encodeJSONPtr(spec.Output); err != nil {
		return fmt.Errorf("encoding output of custom command %q: %w", c.Name, err)
	}
	return nil
}

func decodeJSONColumn[T any](raw string, out *T) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

func decodeJSONPtr[T any](raw string, out **T) error {
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

func encodeJSONColumn[T any](v T) (string, error) {
	if isEmptyJSON(v) {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func encodeJSONPtr[T any](v *T) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func isEmptyJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []CommandParameterM:
		return len(t) == 0
	default:
		return false
	}
}
