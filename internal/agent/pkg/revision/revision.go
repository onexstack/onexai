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

// Package revision compiles the values that decide how a run behaves into one
// immutable, digest-addressed revision. A session pins a revision, so editing
// an agent changes the next session rather than the one already in progress.
package revision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// Input is every value that can change a run's behaviour and nothing else.
// Provenance (generation, timestamps) is deliberately absent: two revisions
// with the same input must be interchangeable.
type Input struct {
	Namespace string   `json:"namespace"`
	Agents    []string `json:"agents"`
	Model     string   `json:"model"`
	// Instruction is the rendered system prompt.
	Instruction    string   `json:"instruction"`
	MCPs           []string `json:"mcps"`
	Skills         []string `json:"skills"`
	KnowledgeBases []string `json:"knowledgeBases"`
	Approval       []string `json:"approval"`
	CustomCommands []string `json:"customCommands"`
	Webhooks       []string `json:"webhooks"`
	OutputSchema   string   `json:"outputSchema,omitempty"`
	OutputStrict   bool     `json:"outputSchemaStrict,omitempty"`
	MemoryEnabled  bool     `json:"memoryEnabled,omitempty"`
	MemoryModel    string   `json:"memoryEmbeddingModel,omitempty"`
	// RuntimeProfile and RuntimeKind make the runtime part of the identity: a
	// session pinned to a revision cannot silently move to another runtime.
	RuntimeProfile string `json:"runtimeProfile,omitempty"`
	RuntimeKind    string `json:"runtimeKind,omitempty"`
}

// Revision is the compiled identity.
type Revision struct {
	ID     string
	Digest string
	Spec   string
}

// Compile normalizes the input, hashes it, and derives an ID from the digest.
// The ID is a name-based UUID, so compiling the same input twice yields the
// same ID on any process.
func Compile(input Input) (Revision, error) {
	normalized := input
	normalized.Agents = sortedUnique(input.Agents)
	normalized.MCPs = sortedUnique(input.MCPs)
	normalized.Skills = sortedUnique(input.Skills)
	normalized.KnowledgeBases = sortedUnique(input.KnowledgeBases)
	normalized.Approval = sortedUnique(input.Approval)
	normalized.CustomCommands = sortedUnique(input.CustomCommands)
	normalized.Webhooks = sortedUnique(input.Webhooks)

	encoded, err := json.Marshal(normalized)
	if err != nil {
		return Revision{}, fmt.Errorf("revision: encode input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(digest)).String()
	return Revision{ID: id, Digest: digest, Spec: string(encoded)}, nil
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	slices.Sort(out)
	return slices.Compact(out)
}
