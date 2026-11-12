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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// A prompt's namespace is part of the record, not a request-scoped detail: a
// console round-trip must hand it back unchanged.
func TestPromptNamespaceRoundTrips(t *testing.T) {
	row := PromptV1ToPromptModel(&v1.Prompt{Name: "p", Content: "c", Namespace: "team-a"})
	assert.Equal(t, "team-a", row.Namespace)

	wire := PromptModelToPromptV1(&model.PromptM{Name: "p", Content: "c", Namespace: "team-a"})
	assert.Equal(t, "team-a", wire.Namespace)
}

// An empty namespace is the deployment default; conversion must not invent one.
func TestPromptNamespaceEmptyStaysEmpty(t *testing.T) {
	row := PromptV1ToPromptModel(&v1.Prompt{Name: "p"})
	assert.Empty(t, row.Namespace)

	wire := PromptModelToPromptV1(&model.PromptM{Name: "p"})
	assert.Empty(t, wire.Namespace)
}
