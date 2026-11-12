// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package agent

import (
	"testing"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

func TestRunFingerprintIsOrderIndependent(t *testing.T) {
	first := runFingerprint(agentapi.RunOptions{
		Agents: []string{"b", "a"},
		Skills: []string{"lint", "code-review"},
	})
	second := runFingerprint(agentapi.RunOptions{
		Agents: []string{"a", "b"},
		Skills: []string{"code-review", "lint"},
	})
	if first != second {
		t.Errorf("fingerprint depends on list order: %q vs %q", first, second)
	}
}

func TestRunFingerprintChangesWithCapabilities(t *testing.T) {
	base := agentapi.RunOptions{Agents: []string{"a"}, Skills: []string{"lint"}}
	extra := base
	extra.Skills = []string{"lint", "code-review"}

	if runFingerprint(base) == runFingerprint(extra) {
		t.Error("adding a skill must change the fingerprint")
	}

	instruction := base
	instruction.Instruction = "a different prompt"
	if runFingerprint(base) == runFingerprint(instruction) {
		t.Error("changing the instruction must change the fingerprint")
	}
}
