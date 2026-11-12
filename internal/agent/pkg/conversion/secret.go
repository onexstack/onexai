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

// SecretV1ToSecretModel converts a proto secret to the domain model.
func SecretV1ToSecretModel(p *v1.Secret) *model.SecretM {
	if p == nil {
		return &model.SecretM{}
	}
	return &model.SecretM{
		SecretID:    p.GetSecretId(),
		SecretKey:   p.GetSecretKey(),
		Description: p.GetDescription(),
		CreatedAt:   p.GetCreatedAt(),
		UpdatedAt:   p.GetUpdatedAt(),
	}
}

// SecretModelToSecretV1 converts a stored secret to its API form.
//
// The key is emitted only when includeKey is true, which is the create response
// alone: the request carries the client-supplied key, so the creator already has
// it once, and every later read (get/list/update) must not hand it back. The
// console reveals it on demand through GET /v1/core/secrets/:secretId/key, which
// is administrator-only. The AK/SK middleware resolves the key through
// resources.Secrets().Get directly, not through this function, so redaction does
// not affect authentication.
func SecretModelToSecretV1(s *model.SecretM, includeKey bool) *v1.Secret {
	if s == nil {
		return nil
	}
	out := &v1.Secret{
		SecretId:    s.SecretID,
		SecretKey:   "",
		Description: s.Description,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
	}
	if includeKey {
		out.SecretKey = s.SecretKey
	}
	return out
}
