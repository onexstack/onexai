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

package agentapi

import (
	"context"
	"fmt"
	"time"
)

// Decisions a caller may answer an approval interrupt with. They are carried on
// the wire (AgentApproveRequest.decision), so the values themselves are part of
// the API.
const (
	// DecisionAllow runs the pending tools this once.
	DecisionAllow = "allow"
	// DecisionDeny refuses them. The tools do not run: each is handed back to the
	// model as an ordinary tool result saying it was refused, which lets the
	// model explain the refusal and close the turn in its own words. The turn is
	// not aborted — a refusal is an answer, not a failure.
	DecisionDeny = "deny"
	// DecisionAllowAll runs them and stops asking for this run, so a turn that
	// needs several approvals takes only one round trip.
	DecisionAllowAll = "allow_all"
)

// PermissionInfo is the user-facing payload the approval middleware raises when
// it pauses a tool call for a decision. It is carried as the interrupt's Info,
// and its String method is what renders the prompt when the interrupt is
// surfaced to a human without a purpose-built card.
//
// It is persisted with the checkpoint, so it is registered for gob in agent.go
// beside AskQuestionInfo — see the note there. Its fields are therefore part of
// the stored format and may be added to but not renamed.
type PermissionInfo struct {
	// ToolName is the tool as the model called it, which is also the name the
	// policy matched and the name an approver recognises.
	ToolName string
	// CallID is the model's tool-call id, stable across the pause and resume.
	CallID string
	// Arguments is the JSON the model asked to run the tool with, shown to the
	// approver so the decision is informed rather than a rubber stamp.
	Arguments string
	// Reason, when set, is why the call was held: an authorization denial or
	// elevation reason. It is shown to the approver so they know what they are
	// overriding, rather than being asked to rubber-stamp an unexplained
	// prompt. It is empty for a plain require_approval gate.
	Reason string
	// Source says what held the call: SourcePolicy for a require_approval
	// pattern, SourceAuthz for an authorization denial. It only changes how the
	// prompt reads — the answer semantics are identical.
	Source string
}

// Source values for PermissionInfo.Source.
const (
	// SourcePolicy is a gate declared by an approval policy (require_approval).
	SourcePolicy = "policy"
	// SourceAuthz is a gate raised because authorization did not allow the call.
	SourceAuthz = "authz"
)

// String renders the pending call as the prompt a human reads.
func (i *PermissionInfo) String() string {
	if i == nil {
		return "一个工具调用正在等待批准"
	}
	if i.Source == SourceAuthz {
		if i.Reason != "" {
			return fmt.Sprintf("工具 %s 被权限策略拦截（原因：%s），是否授权放行？", i.ToolName, i.Reason)
		}
		return fmt.Sprintf("工具 %s 被权限策略拦截，是否授权放行？", i.ToolName)
	}
	if i.Arguments == "" || i.Arguments == "{}" {
		return fmt.Sprintf("工具 %s 请求执行，是否允许？", i.ToolName)
	}
	return fmt.Sprintf("工具 %s 请求执行，参数：%s。是否允许？", i.ToolName, i.Arguments)
}

// ApprovalResult is a human's answer to one or more PermissionInfo requests. It
// is passed as resume data, which stays in-process, so like PermissionInfo it is
// never serialized and needs no registration.
type ApprovalResult struct {
	// Approved runs the pending tools with the arguments they paused with.
	Approved bool
	// AllowAll additionally stops the run from asking again.
	AllowAll bool
	// Reason, when set, is handed to the model as the refusal's explanation
	// instead of the generic one.
	Reason string
}

// InterruptRequest is one tool call a suspended run is waiting on.
type InterruptRequest struct {
	// Address is the interrupt point's fully-qualified ID. It is what a resume
	// targets, and the framework generates it fresh for each suspension, so it
	// must be read from the interrupt event and never reconstructed.
	Address string
	// ToolName is the tool whose execution is pending.
	ToolName string
	// Question is the prompt shown to the human.
	Question string
	// Kind is InterruptKindPermission or InterruptKindQuestion.
	Kind string
	// Options are the selectable answers for a question interrupt.
	Options []string
	// MultiSelect indicates the user may pick multiple options.
	MultiSelect bool
	// Arguments is the JSON the model asked to run the tool with. It is what a
	// caller shows beside the tool name, so that approving means approving a
	// specific call rather than the tool in general.
	Arguments string
}

// RunMeta is what a resume needs beyond the checkpoint blob itself.
//
// eino's checkpoint holds the agent's execution state, which is enough to
// continue the run but not enough to rebuild it: a run's instruction, tool set
// and approval policy are all resolved from the agent names it was started with,
// and those names are not in the checkpoint. This is stored beside the
// checkpoint, under the same key and the same lifetime, so that resuming a run
// cannot silently rebuild it with a different tool set — a mistake that would
// show up only as a resume failing to find its interrupt point.
type RunMeta struct {
	// Agents is the agent selection the suspended run was started with. It is
	// what resolveInstruction, resolveMCPs and resolveApproval all derive from.
	Agents []string `json:"agents,omitempty"`
	// Model is the effective model endpoint name (agent.model.name) the run was
	// started with. A resume must rebuild against the same one.
	Model string `json:"model,omitempty"`
	// Owned reports that Model is a user-configured endpoint, so the resumed
	// half does not charge the platform token budget either.
	Owned bool `json:"owned,omitempty"`
	// Deadline is the run's own timeout, recorded so a resumed half uses the
	// same bound rather than the deployment default.
	Deadline time.Duration `json:"deadline,omitempty"`
	// SessionID is the conversation memory bucket the run belongs to.
	SessionID string `json:"sessionId,omitempty"`
	// UserMessage is the user turn the suspended run is answering, kept so the
	// exchange can be persisted once the resumed run finally finishes. It has no
	// omitempty: Message is a struct, whose zero value json would encode as an
	// empty object rather than omitting the key, so the tag would claim an
	// absence it cannot produce.
	UserMessage Message `json:"userMessage"`
	// UserID is the asker's stable identity, restored onto the resumed run's
	// context so MCP tools still attribute their work to the asker, and so
	// authorization evaluates the resumed half under the same subject.
	UserID string `json:"userId,omitempty"`
	// Reasoning is whether the run's stream surfaces reasoning, so the resumed
	// stream renders the same panels the interrupted one did.
	Reasoning bool `json:"reasoning,omitempty"`
	// Metering is who meters the run, so the resumed half answers the same way
	// the interrupted one did. The approve request has no field for it and the
	// resumed half is rebuilt from this metadata alone, so recording it is the
	// only way the decision survives the pause.
	Metering Metering `json:"metering,omitempty"`
	// AllowAll records that an earlier answer was DecisionAllowAll, so the
	// resumed run does not ask again. It lives here rather than in the engine's
	// memory because the resume may land on a different pod.
	AllowAll bool `json:"allowAll,omitempty"`
	// ApprovalFallback is what the paused run did about gated tools it could not
	// ask about. It is recorded for the same reason as everything else here: the
	// resumed run is rebuilt from this metadata, and a resumed run whose gates
	// started pausing would ask a question the original caller never could have
	// answered.
	ApprovalFallback string `json:"approvalFallback,omitempty"`
	// RequestID identifies the suspension now awaiting an answer. A resume must
	// name it: one run can suspend several times, so a card answered after the
	// run has moved on must be refused rather than applied to whatever the run
	// happens to be waiting on now.
	RequestID string `json:"requestId,omitempty"`
	// Pending are the interrupt points that suspension is waiting on. They are
	// recorded here rather than accepted from the caller so that a resume cannot
	// name an address the run never paused at.
	Pending []PendingAddress `json:"pending,omitempty"`
	// ToolTrail is the tool calls the run made before it paused, already rendered
	// as rows. A resumed run replays none of them, so without this the record
	// written when the turn finally ends would describe only the half that ran
	// after the answer and read as the whole of it.
	ToolTrail []string `json:"toolTrail,omitempty"`
	// Skills and KnowledgeBases are the resource names the suspended run was
	// built with, recorded rather than re-derived from Agents on resume.
	//
	// Re-deriving would be enough while nothing changes, but a human can take
	// minutes to answer: an agent edited in that window would give the resumed
	// half a different tool set, and the checkpoint addresses the interrupt is
	// recorded under would no longer line up with the tools answering it. Empty
	// means "not recorded" — a checkpoint written before these fields existed —
	// and the resume falls back to re-deriving.
	Skills         []string `json:"skills,omitempty"`
	KnowledgeBases []string `json:"knowledgeBases,omitempty"`
	// Capabilities is the narrowing the suspended run was started with, recorded
	// so the resumed half runs with the same subset even though the resume
	// request has no field for it. Nil means the run was not narrowed.
	//
	// The pointer matters here too: a selection narrowed to nothing must not be
	// read back as "not recorded, use the full effective set".
	Capabilities *CapabilitySelection `json:"capabilities,omitempty"`
	// Composition is the composed run's replacement set, recorded for the same
	// reason: the resumed half is rebuilt from this metadata, and a composed
	// run whose continuation silently fell back to the host agent's bindings
	// would execute a different agent than the one that paused.
	//
	// Nil means the run was not composed. Composed and narrowed are mutually
	// exclusive, so at most one of Capabilities and Composition is set.
	Composition *AgentComposition `json:"composition,omitempty"`
	// Source is the surface the run arrived on, carried so the audit record
	// written when the resumed half finishes names the same surface the
	// original request did.
	Source string `json:"source,omitempty"`
	// SenderName is the original asker's display name, recorded so the audit row
	// the resumed half writes names the same person the original request did.
	SenderName string `json:"senderName,omitempty"`
	// SubjectKind is what the asker was, recorded beside UserID because a name
	// alone cannot say whether the resumed half speaks for a person or a service.
	SubjectKind string `json:"subjectKind,omitempty"`
	// Turn is the run's ordinal among the conversation's user turns, recorded
	// so a resumed half auto-saves at the same point the interrupted one would
	// have: the resumed half is the same turn, not a new one.
	Turn int `json:"turn,omitempty"`
	// Lineage is the delegation chain the suspended run belongs to. A resumed
	// half that restarted the chain at depth zero would let an agent that
	// delegates in a cycle escape the depth bound by pausing each time.
	Lineage *Lineage `json:"lineage,omitempty"`
	// ActorID and ActorName are the party that started the run when it is not
	// the party it executes as, carried so both halves of one interrupted turn
	// name the same person.
	ActorID   string `json:"actorId,omitempty"`
	ActorName string `json:"actorName,omitempty"`
	// OutputSchema is the canonical structured-output contract the run must
	// satisfy. Empty means a plain-text answer.
	OutputSchema string `json:"outputSchema,omitempty"`
	// OutputSchemaStrict asks the provider to constrain decoding.
	OutputSchemaStrict bool `json:"outputSchemaStrict,omitempty"`
	// RuntimeKind is the runtime the run executes on. It is recorded so a
	// resumed half writes its run row on the same runtime rather than inferring
	// it from a profile that may have changed while the pause waited.
	RuntimeKind string `json:"runtimeKind,omitempty"`
	// StartedAt is when the original request was accepted. A resumed run
	// measures its own turn, but the reply a human sees took the whole time
	// including the pause, and that is the number worth recording.
	StartedAt int64 `json:"startedAt,omitempty"` // unix nanos
}

// PendingAddress is one interrupt point a suspended run is waiting on.
type PendingAddress struct {
	// Address is the fully-qualified interrupt ID used as a resume target.
	Address string `json:"address"`
	// Kind is InterruptKindPermission or InterruptKindQuestion, which decides
	// what the resume must supply for this address.
	Kind string `json:"kind"`
	// ToolName is the tool behind the address, for logs and for refusal text.
	ToolName string `json:"toolName,omitempty"`
}

// ResumeOptions is what an engine needs to continue a suspended run.
type ResumeOptions struct {
	// Run carries the same per-run knobs the suspended run was started with.
	// Rebuilding the agent identically is not a nicety: the checkpoint records
	// the interrupt point by address, and a different tool set would leave the
	// resume with nothing to answer.
	Run RunOptions
	// RequestID is the suspension being answered.
	RequestID string
	// Decision is DecisionAllow, DecisionDeny or DecisionAllowAll, applied to
	// every permission interrupt in the suspension.
	Decision string
	// Answers supplies the text for question interrupts, keyed by address.
	Answers map[string]string
	// Reason optionally explains a refusal. It is handed to the model in the
	// refused tool's result, so the model can say why the call did not run
	// instead of leaving the asker with a bare "no".
	Reason string
	// Pending are the recorded interrupt points of that suspension.
	Pending []PendingAddress
	// AllowAll says this run has already been told to stop asking, so the
	// resumed run must not raise another approval interrupt. It is true when
	// this answer is itself DecisionAllowAll and on every later resume of the
	// same run, which is why it is carried separately from Decision.
	AllowAll bool
}

// Resumable is implemented by engines that can continue a suspended run.
//
// It is separate from Engine rather than part of it because only the eino engine
// checkpoints: the OpenAI-compatible engine answers a single stateless turn and
// has no state to continue, so it never raises an interrupt to be answered.
// Callers type-assert for it, as they do for the stop registry's subscriber.
type Resumable interface {
	Resume(ctx context.Context, opts ResumeOptions) (<-chan Event, error)
}
