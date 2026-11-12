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

package conversion

import (
	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ModelV1ToModelModel converts a proto model endpoint to the domain model.
func ModelV1ToModelModel(p *v1.Model) *model.ModelM {
	if p == nil {
		return &model.ModelM{}
	}
	return &model.ModelM{
		Namespace:    p.GetNamespace(),
		Name:         p.GetName(),
		DisplayName:  p.GetDisplayName(),
		Status:       p.GetStatus(),
		Description:  p.GetDescription(),
		Owner:        p.GetOwner(),
		Provider:     p.GetProvider(),
		BaseURL:      p.GetBaseUrl(),
		APIKey:       p.GetApiKey(),
		Model:        p.GetModel(),
		Labels:       p.GetLabels(),
		Capabilities: p.GetCapabilities(),
		CreatedAt:    p.GetCreatedAt(),
		UpdatedAt:    p.GetUpdatedAt(),
	}
}

// ModelModelToModelV1 maps a model row onto the contract message.
//
// # The API key is not a read field
//
// It is the same kind of value as a secret's: the creator sees it once, and Get
// or List never return it. A model row holds an upstream provider's key — an LLM
// credential that bills — and the catalog is readable by every signed-in user,
// so returning it from a list would hand every caller the key rather than only
// the one that set it. HasAPIKey is the read-safe answer to "is one there?".
//
// The console reveals it on demand through GET /v1/agent/models/:name/api-key,
// which is limited to the row's owner or an administrator. That endpoint does
// not go through this function.
//
// There is deliberately no "include the key" switch. The row holds the sealed
// storage form, which is not a usable credential; the one place a caller sees
// the plaintext is the create response, which sets it explicitly from the
// submitted request (see ModelBiz.Create).
func ModelModelToModelV1(m *model.ModelM) *v1.Model {
	if m == nil {
		return nil
	}
	return &v1.Model{
		Namespace:    m.Namespace,
		Name:         m.Name,
		DisplayName:  m.DisplayName,
		Status:       StatusToV1(m.Status),
		Description:  m.Description,
		Owner:        m.Owner,
		Provider:     m.Provider,
		BaseUrl:      m.BaseURL,
		ApiKey:       "",
		HasAPIKey:    m.APIKey != "",
		Model:        m.Model,
		Labels:       m.Labels,
		Capabilities: m.Capabilities,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}
