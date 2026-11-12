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

package agent

import (
	"strings"
	"testing"
)

// TestApplyInvokedSkills covers what an explicitly invoked skill does to a run.
//
// A command is the asker saying "run this", so the run is told what was asked
// for rather than left to infer it from the question — which is the whole
// difference between a slash command and a sentence that mentions one.
func TestApplyInvokedSkills(t *testing.T) {
	const persona = "你是一个运维助手。"

	t.Run("a run that invokes nothing is unchanged", func(t *testing.T) {
		got, err := applyInvokedSkills(persona, []string{"k8s-troubleshoot"}, nil)
		if err != nil {
			t.Fatalf("applyInvokedSkills = %v", err)
		}
		if got != persona {
			t.Errorf("instruction = %q, want it untouched", got)
		}
	})

	t.Run("a declared skill is named ahead of the persona", func(t *testing.T) {
		got, err := applyInvokedSkills(persona, []string{"k8s-troubleshoot"}, []string{"k8s-troubleshoot"})
		if err != nil {
			t.Fatalf("applyInvokedSkills = %v", err)
		}
		if !strings.Contains(got, "k8s-troubleshoot") {
			t.Errorf("instruction = %q, want it to name the skill", got)
		}
		if !strings.HasSuffix(got, persona) {
			t.Errorf("instruction = %q, want the persona to survive", got)
		}
		if !strings.HasPrefix(got, "本次请求") {
			t.Errorf("instruction = %q, want the directive first", got)
		}
	})

	t.Run("an empty persona is replaced by the directive alone", func(t *testing.T) {
		got, err := applyInvokedSkills("", []string{"k8s-troubleshoot"}, []string{"k8s-troubleshoot"})
		if err != nil {
			t.Fatalf("applyInvokedSkills = %v", err)
		}
		if got == "" || strings.HasPrefix(got, "\n") {
			t.Errorf("instruction = %q, want the directive with nothing prepended to it", got)
		}
	})

	// The refusal is the half that matters: the menu a person clicked and the
	// run that answers read the same declaration, so a name outside it means
	// one of the two is looking at something that has changed. Answering anyway
	// would present a command that silently does nothing.
	t.Run("a skill the selection does not declare is refused", func(t *testing.T) {
		got, err := applyInvokedSkills(persona, []string{"weekly-report"}, []string{"k8s-troubleshoot"})
		if err == nil {
			t.Fatal("applyInvokedSkills accepted a skill the agents do not declare")
		}
		if got != "" {
			t.Errorf("instruction = %q, want none returned alongside the error", got)
		}
		if !strings.Contains(err.Error(), "k8s-troubleshoot") {
			t.Errorf("error = %v, want it to name the skill", err)
		}
	})

	t.Run("one undeclared name refuses the whole invocation", func(t *testing.T) {
		_, err := applyInvokedSkills(persona,
			[]string{"k8s-troubleshoot", "weekly-report"},
			[]string{"k8s-troubleshoot", "typo"})
		if err == nil {
			t.Fatal("applyInvokedSkills accepted an invocation with an undeclared name in it")
		}
		if !strings.Contains(err.Error(), "typo") {
			t.Errorf("error = %v, want it to name the offending skill", err)
		}
	})
}
