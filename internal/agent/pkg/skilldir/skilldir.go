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

// Package skilldir owns the on-disk layout of agent-isolated skills.
//
// It is a leaf: it depends on nothing but the standard library, because three
// different layers need the same path and none of them should have to import
// the others to get it —
//
//   - the skill backends persist files there,
//   - the agent builder tells the model that path is the project root and hands
//     the same path to the sandbox as its allow-list,
//   - and the tests that pin those three together live in yet another package.
//
// One definition, three readers, and no way for two of them to drift.
package skilldir

import (
	"path/filepath"
	"strings"
)

// sharedSegment is used when there is no agent key. It keeps the path non-empty
// and preserves the shared-cache behaviour for that case.
const sharedSegment = "__shared__"

// AgentDirSegment converts an agent key into a single, filesystem-safe
// directory segment.
//
// Path separators and traversal sequences are stripped rather than rejected: a
// malformed or hostile agent name must not be able to escape the skills root,
// and the name comes from stored configuration rather than from code.
func AgentDirSegment(agentKey string) string {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return sharedSegment
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_")
	return replacer.Replace(agentKey)
}

// Isolated returns the working directory of one agent set under skillsDir.
func Isolated(skillsDir, agentKey string) string {
	return filepath.Join(skillsDir, AgentDirSegment(agentKey))
}
