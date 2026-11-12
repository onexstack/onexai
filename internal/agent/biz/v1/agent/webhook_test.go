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
	"errors"
	"testing"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// TestIsAllowanceCodePicksTheStatusCode pins which refusals answer 429 and
// which answer 403: the difference is whether a client should try again, so it
// is not a cosmetic one.
func TestIsAllowanceCodePicksTheStatusCode(t *testing.T) {
	for _, tc := range []struct {
		code string
		want bool
		why  string
	}{
		{"", true, "an unnamed code is likelier a budget gate, and 429 is the safer default"},
		{"quotaExceeded", true, "a spending condition"},
		{"tierRateLimited", true, "the platform's own word for an exhausted tier"},
		{"tokenBudgetExceeded", true, "a spending condition"},
		{"notEntitled", false, "a permission, not an allowance"},
		{"maintenanceWindow", false, "a refusal that retrying will not fix"},
	} {
		if got := isAllowanceCode(tc.code); got != tc.want {
			t.Errorf("isAllowanceCode(%q) = %v, want %v (%s)", tc.code, got, tc.want, tc.why)
		}
	}
}

// TestWebhookSubjectKindMarksA2AAsAService pins the one surface whose asker is
// not a person: its sender is a fixed constant rather than an account, so a
// hook that treats it as one would attribute the traffic to nobody.
func TestWebhookSubjectKindMarksA2AAsAService(t *testing.T) {
	if got := webhookSubjectKind(sourceA2A); got != "serviceaccount" {
		t.Errorf("webhookSubjectKind(%q) = %q, want serviceaccount", sourceA2A, got)
	}
	for _, surface := range []string{"chat", "api", "console", ""} {
		if got := webhookSubjectKind(surface); got != "user" {
			t.Errorf("webhookSubjectKind(%q) = %q, want user", surface, got)
		}
	}
}

// TestWebhookUsageCarriesTheLedgersFiveNumbers pins the payload's usage block
// against the platform's own usage vocabulary: the two are read side by side, so
// a field renamed on one side is a field that silently reads zero on the other.
func TestWebhookUsageCarriesTheLedgersFiveNumbers(t *testing.T) {
	got := webhookUsage(&agentapi.Usage{
		Calls: 3, PromptTokens: 1234, CompletionTokens: 567,
		TotalTokens: 1801, ReasoningTokens: 100,
		CacheHitTokens: 900, CacheMissTokens: 334,
	})
	if got.Calls != 3 || got.PromptTokens != 1234 || got.CompletionTokens != 567 {
		t.Errorf("usage = %+v, want the run's calls and token counts", got)
	}
	if got.CacheHitTokens != 900 || got.CacheMissTokens != 334 {
		t.Errorf("usage = %+v, want the cache split carried through", got)
	}

	// A run that made no model call reports zeroes rather than being skipped:
	// the receiver has to be able to tell "no calls" from "no field".
	if got := webhookUsage(nil); got.Calls != 0 || got.PromptTokens != 0 {
		t.Errorf("webhookUsage(nil) = %+v, want zeroes", got)
	}
}

// TestRunRefusedErrorCarriesTheHooksMessage: the message is what the person
// whose run was refused is shown, so it has to survive the trip out of the biz
// layer unchanged.
func TestRunRefusedErrorCarriesTheHooksMessage(t *testing.T) {
	err := error(&RunRefusedError{Message: "该 agent 暂不对外开放", Hook: "onex-policy"})
	var refused *RunRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("errors.As failed for %T", err)
	}
	if refused.Message != "该 agent 暂不对外开放" {
		t.Errorf("message = %q, want the hook's own words", refused.Message)
	}
	if err.Error() != "该 agent 暂不对外开放" {
		t.Errorf("Error() = %q, want the message alone", err.Error())
	}
}
