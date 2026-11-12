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

package bot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"fmt"

	agentpkg "github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/pkg/clientset/agent"
)

// permissionRefusal is the refusal oai-agent-apiserver returns for a subject that may
// not use the agent, spelled the way the server spells it so this test fails if
// the client ever stops reading the envelope. The subject id is in it on
// purpose: a card must never show it.
var permissionRefusal = &agent.HTTPError{
	StatusCode: http.StatusForbidden,
	Reason:     "PermissionDenied",
	Message:    `agent "default" access denied for subject ou_684c62db025820b3af793e43a8b549ea: agent: authorization: all levels NoOpinion (fail-closed)`,
}

// TestRunAITurnShowsARefusalAsANotice pins the card a refused turn produces: it
// names what happened and what to do, in place of the answer that never came,
// and the footer says 权限不足 (insufficient permission) rather than the generic 失败 (failed) — which reads as a
// broken agent for something the user can act on.
func TestRunAITurnShowsARefusalAsANotice(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)
	f.llm.askErr = permissionRefusal

	if err := f.biz.runAI(context.Background(), f.dr, "请删除 Pod", nil); err != nil {
		t.Fatalf("a refused turn returned an error: %v", err)
	}

	final, ok := f.lark.lastClosedCard()
	if !ok {
		t.Fatal("the refused turn closed no card")
	}
	if !strings.Contains(final, "无法使用该 Agent") {
		t.Errorf("the card does not say the agent could not be used:\n%s", final)
	}
	if !strings.Contains(final, "权限不足") {
		t.Errorf("the footer does not name the refusal:\n%s", final)
	}
	if strings.Contains(final, "失败") {
		t.Errorf("the card reports a failure for a refusal:\n%s", final)
	}
	// The server's own message is an operator's: it is English and it names an
	// internal subject id, so it belongs in the log rather than on the card.
	if strings.Contains(final, "ou_684c") || strings.Contains(final, "access denied") {
		t.Errorf("the card leaks the backend's explanation:\n%s", final)
	}
}

// TestRunAITurnReportsACredentialFailureAsItsOwnProblem pins the other half of
// the pair: a 401 is this deployment's fault, so the card says so and does not
// tell the user their permissions are at issue. The operator-facing half — the
// Error-level log and the backend_unauthenticated metric — is what
// refusePermission records; the turn's returned error is discarded by the
// session that runs it, so it is deliberately not the signal.
func TestRunAITurnReportsACredentialFailureAsItsOwnProblem(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)
	f.llm.askErr = &agent.HTTPError{
		StatusCode: http.StatusUnauthorized,
		Reason:     "Unauthenticated",
		Message:    "signature verification failed",
	}

	if err := f.biz.runAI(context.Background(), f.dr, "请删除 Pod", nil); err != nil {
		t.Fatalf("a credential failure failed the turn: %v", err)
	}

	final, ok := f.lark.lastClosedCard()
	if !ok {
		t.Fatal("the failed turn closed no card")
	}
	if !strings.Contains(final, "服务认证失败") {
		t.Errorf("the card does not name the credential failure:\n%s", final)
	}
	if strings.Contains(final, "无法使用该 Agent") {
		t.Errorf("a deployment fault was reported as the user's permissions:\n%s", final)
	}
	if strings.Contains(final, "signature verification failed") {
		t.Errorf("the card leaks the backend's explanation:\n%s", final)
	}
}

// TestARefusedApprovalIsShownTheSameWay pins the second path to the same card.
// Resuming a paused run re-runs the gate, so an answer can be refused after a
// card has already been rendered and answered — and that refusal arrives as a
// stream event rather than as the ask's error, which is the branch most easily
// missed.
func TestARefusedApprovalIsShownTheSameWay(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)
	f.llm.approveErr = permissionRefusal

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	if _, err := f.biz.ProcessCard(context.Background(), decisionClick(permClickValue(PermAllow, map[string]interface{}{
		"run_id":       turn.runID,
		"req_id":       "req-1",
		"perm_label":   labelAllowed,
		"perm_subject": "pipeline",
	}))); err != nil {
		t.Fatalf("the click failed: %v", err)
	}

	waitFor(t, "the refused continuation to close the card", func() bool { return f.lark.closedCount() == 1 })
	final, ok := f.lark.lastClosedCard()
	if !ok {
		t.Fatal("the refused continuation closed no card")
	}
	if !strings.Contains(final, "无法使用该 Agent") || !strings.Contains(final, "权限不足") {
		t.Errorf("the refused approval did not get the refusal card:\n%s", final)
	}
}

// TestClassifyPermissionStaysOffEverythingElse keeps a server fault and an
// unrelated error from being dressed up as a permission problem: only the two
// refusals have a card of their own.
func TestClassifyPermissionStaysOffEverythingElse(t *testing.T) {
	if _, ok := classifyPermission(&agent.HTTPError{StatusCode: http.StatusInternalServerError}); ok {
		t.Error("a server fault was classified as a permission refusal")
	}
	if _, ok := classifyPermission(errors.New("connection reset")); ok {
		t.Error("an unrelated error was classified as a permission refusal")
	}
}

// TestStopMarkerNamesTheRefusals pins the footer words, which are the only
// thing that distinguishes a refusal from a failure on a card with no header.
func TestStopMarkerNamesTheRefusals(t *testing.T) {
	if got := stopMarker(false, reasonPermission); got != "权限不足" {
		t.Errorf("stopMarker(permission) = %q, want 权限不足", got)
	}
	if got := stopMarker(false, reasonUnauthorized); got != "服务认证失败" {
		t.Errorf("stopMarker(unauthorized) = %q, want 服务认证失败", got)
	}
	if got := stopMarker(false, reasonQuotaExceeded); got != reasonQuotaExceeded {
		t.Errorf("stopMarker(quota) = %q, want %q", got, reasonQuotaExceeded)
	}
	// The fallback is what a refusal with no marker of its own reads as, and it
	// is exactly what an allowance refusal must not become.
	if got := stopMarker(false, "somethingNew"); got != "失败" {
		t.Errorf("stopMarker(unknown) = %q, want 失败", got)
	}
}

// TestAnAllowanceRefusalShowsTheBackendsOwnSentence pins the one refusal whose
// backend text IS the card. The permission refusals above are suppressed
// because theirs name internal subjects; this one is written for the reader —
// by whichever side knows which allowance ran out, a run-lifecycle hook
// included — and suppressing it would leave the person with nothing to act on.
func TestAnAllowanceRefusalShowsTheBackendsOwnSentence(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)
	f.llm.askErr = &agent.HTTPError{
		StatusCode: http.StatusTooManyRequests,
		Reason:     "TooManyRequests",
		Message:    "今日 AI 用量已达上限，请明天再试或升级套餐",
	}

	if err := f.biz.runAI(context.Background(), f.dr, "帮我看看这个服务", nil); err != nil {
		t.Fatalf("a refused turn returned an error: %v", err)
	}
	final, ok := f.lark.lastClosedCard()
	if !ok {
		t.Fatal("the refused turn closed no card")
	}
	if !strings.Contains(final, "今日 AI 用量已达上限") {
		t.Errorf("the card does not carry the backend's own sentence:\n%s", final)
	}
	if !strings.Contains(final, reasonQuotaExceeded) {
		t.Errorf("the footer does not name the allowance:\n%s", final)
	}
	if strings.Contains(final, "权限不足") {
		t.Errorf("an allowance refusal was reported as a permission problem:\n%s", final)
	}
	// A refusal with time on it is not a failure either: the deployment is
	// working, and the run that was refused never started.
	if strings.Contains(final, "失败") {
		t.Errorf("the card reports a failure for a refusal:\n%s", final)
	}
}

// TestAnAllowanceRefusalWithoutASentenceStillReads covers a body that did not
// survive the trip: an empty card would be worse than a generic sentence.
func TestAnAllowanceRefusalWithoutASentenceStillReads(t *testing.T) {
	notice, ok := classifyPermission(fmt.Errorf("%w: %w", agentpkg.ErrQuotaExceeded,
		&agent.HTTPError{StatusCode: http.StatusTooManyRequests}))
	if !ok {
		t.Fatal("an allowance refusal was not classified")
	}
	if !strings.Contains(notice.body, "用量已达上限") {
		t.Errorf("body = %q, want the fallback sentence", notice.body)
	}
	if notice.metric != "backend_quota_exceeded" {
		t.Errorf("metric = %q, want backend_quota_exceeded", notice.metric)
	}
}
