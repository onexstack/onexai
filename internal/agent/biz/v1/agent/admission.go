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
	"context"
	"fmt"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// runIdentity is what both entry points know about a run before it starts: the
// fields the run's bound context is built from, plus the selection it executes.
//
// It exists so Ask and Approve bind a run the same way. Approve reads these off
// the checkpoint record and Ask off the request, but the shape is one, which is
// what makes "the resumed half is the same run" checkable.
type runIdentity struct {
	RunID       string
	SessionID   string
	Deadline    time.Duration
	SubjectID   string
	SubjectName string
	SubjectKind string
	Agents      []string
	// Capabilities, when non-nil, narrows the run to these names. Nil is the
	// full effective set. Ask takes it off the request; Approve off the
	// checkpoint record.
	Capabilities *agentapi.CapabilitySelection
	// CapabilitiesRecorded marks a selection that came off a checkpoint. Those
	// names are intersected with what the agent resolves to now rather than
	// validated: a resource revoked while the pause waited is dropped, not an
	// error that leaves the human a card they cannot answer.
	CapabilitiesRecorded bool
	// Composition, when non-nil, replaces the selection's instruction and
	// capabilities for this run. Mutually exclusive with Capabilities.
	Composition *agentapi.AgentComposition
	// CompositionRecorded marks a composition that came off a checkpoint. Its
	// resources are re-validated leniently: one revoked while the pause waited
	// is dropped rather than making the pause unanswerable.
	CompositionRecorded bool
}

// admitRun binds the run's identity and bounds, authorizes it, and resolves the
// selection it will execute — the part of a run that does not depend on how it
// arrived.
//
// The order is the contract:
//
//   - bind first, because authorization needs the run's subject in hand and the
//     caller's deferred unwind needs a context to close;
//   - authorization before any work, and before Approve claims the answer: a
//     claim is one-shot, so a refusal after claiming would leave the human a
//     card that can no longer be answered;
//   - one selection load, so the instruction, the resource scopes and the
//     approval patterns all describe the same agents;
//   - the root span last, so every span the run produces joins one trace.
//
// The lifecycle hook is deliberately not here: Ask fires it after resolving the
// model and the turn's instruction, while Approve fires it immediately. Each
// caller keeps its own call site so the hook's position does not change.
func (b *AgentBiz) admitRun(
	ctx context.Context, lease *runLease, id runIdentity,
) (context.Context, resolvedSelection, error) {
	_, subj := lease.bind(
		id.RunID, id.Deadline, id.SubjectID, id.SubjectName, id.SubjectKind, id.Agents,
	)
	// A composition replaces the host's prompt and resources, so it takes its
	// own admission path: the host gate still applies, but the resource checks
	// are against the composed set, and the host's instruction/policy are not
	// resolved at all (an unused host resource must not block the run).
	if id.Composition != nil {
		return b.admitComposedRun(ctx, lease, id, subj)
	}

	// The request context, not the run context: authorization judges the caller
	// that asked, and a refusal is reported on the request that caused it.
	if err := authorizeRun(ctx, b.deps.Authz, b.deps.Resources, subj, id.Agents, b.deps.AgentAuthz); err != nil {
		return nil, resolvedSelection{}, err
	}
	sel, err := b.resolveRunSelection(ctx, id.Agents, id.SessionID, resolveOptions{})
	if err != nil {
		return nil, resolvedSelection{}, err
	}
	// An Agent that names authentication Webhooks is only reachable by a caller
	// one of those hooks authenticated. The edge middleware already made that
	// choice; this is the per-Agent half, which a merged selection (or an
	// internal caller reaching Ask directly) must not bypass. AK/SK is exempt:
	// it is the platform's service credential.
	if err := enforceAgentAuthn(ctx, sel.set.records); err != nil {
		return nil, resolvedSelection{}, err
	}
	// Narrowed here, once, so every consumer of the selection — RunOptions, the
	// capability token, the revision digest — describes the same subset. A
	// refusal is returned before the engine is reached, so no money is spent.
	if err := applyCapabilitySelection(&sel, id.Capabilities, !id.CapabilitiesRecorded); err != nil {
		return nil, resolvedSelection{}, err
	}
	return lease.startSpan(id.RunID, id.SessionID, id.SubjectID, id.Agents), sel, nil
}

// admitComposedRun is admitRun for a run whose capabilities come from a
// composition rather than from the host agent.
//
// A composed run names zero or one host. Zero is the hostless playground: there
// is no agent row, so there is no host gate, no inherited instruction/policy and
// no host runtime. One is the original debug run: the host gate first (may this
// subject run this agent at all), then a resolution that deliberately skips the
// host's prompt and namespace policy, then the composition (validate and
// replace), then the composed resources' authorization and namespace policy. A
// failure at any point returns before the engine is reached, so no money is
// spent.
func (b *AgentBiz) admitComposedRun(
	ctx context.Context, lease *runLease, id runIdentity, subj authz.Subject,
) (context.Context, resolvedSelection, error) {
	if len(id.Agents) > 1 || (len(id.Agents) == 1 && id.Agents[0] == "") {
		return nil, resolvedSelection{}, &CompositionError{
			Message: "a composed run names at most one host agent",
		}
	}
	if id.Capabilities != nil {
		// Validation rejects this; kept as a second gate because the two fields
		// mean different things and a handler bug must not merge them.
		return nil, resolvedSelection{}, &CompositionError{
			Message: "a run cannot both narrow and replace an agent's capabilities",
		}
	}
	if len(id.Agents) == 0 {
		return b.admitStandaloneComposedRun(ctx, lease, id, subj)
	}
	host := id.Agents[0]
	if _, err := authorizeAgentGate(ctx, b.deps.Authz, b.deps.Resources, subj, host); err != nil {
		return nil, resolvedSelection{}, err
	}
	sel, err := b.resolveRunSelection(ctx, id.Agents, id.SessionID, resolveOptions{
		skipInstruction:    true,
		skipTargetPolicies: true,
	})
	if err != nil {
		return nil, resolvedSelection{}, err
	}
	if len(sel.set.records) == 0 {
		return nil, resolvedSelection{}, &CompositionError{
			Message: fmt.Sprintf("host agent %q does not exist", host),
		}
	}
	// A hosted run's referrer is the host agent. Letting the request name a
	// different namespace would let one tenant's composition be judged against
	// another tenant's grants.
	referrer := model.NormalizeNamespace(sel.set.records[0].Namespace)
	if id.Composition != nil {
		requested := sharing.Normalize(id.Composition.Namespace)
		if id.Composition.Namespace != "" && requested != referrer {
			return nil, resolvedSelection{}, &CompositionError{Message: fmt.Sprintf(
				"composition.namespace %q disagrees with host agent %q in namespace %q",
				id.Composition.Namespace, host, referrer,
			)}
		}
	}
	if err := b.applyComposition(ctx, &sel, id.Composition, !id.CompositionRecorded, referrer); err != nil {
		return nil, resolvedSelection{}, err
	}
	if err := authorizeComposition(ctx, b.deps.Authz, subj, host, sel.spec); err != nil {
		return nil, resolvedSelection{}, err
	}
	if err := b.resolver.checkReferencePolicies(
		ctx, sel.set.records, referrerNamespaces(sel.set.records), sel.spec, true,
	); err != nil {
		return nil, resolvedSelection{}, err
	}
	return lease.startSpan(id.RunID, id.SessionID, id.SubjectID, id.Agents), sel, nil
}

// admitStandaloneComposedRun is admitComposedRun for a composition with no host.
//
// There is no agent row to gate, resolve or inherit from: the caller is the
// subject, the base selection is empty, and the composition is the whole
// configuration. Resource references are still authorized against the subject
// (with an empty agent name, so only rules that apply to every agent match), and
// restricted resources are still refused by the hostless namespace check. The
// absence of an agent is a deliberate product decision, not an authorization
// bypass.
func (b *AgentBiz) admitStandaloneComposedRun(
	ctx context.Context, lease *runLease, id runIdentity, subj authz.Subject,
) (context.Context, resolvedSelection, error) {
	sel, err := b.resolveRunSelection(ctx, nil, id.SessionID, resolveOptions{
		skipInstruction:    true,
		skipTargetPolicies: true,
	})
	if err != nil {
		return nil, resolvedSelection{}, err
	}
	// No host means no agent namespace: the caller's business namespace is the
	// referrer. An empty value falls back to the deployment default, which is
	// what a client that predates the field sends.
	referrer := sharing.Normalize(id.Composition.Namespace)
	if err := b.applyComposition(ctx, &sel, id.Composition, !id.CompositionRecorded, referrer); err != nil {
		return nil, resolvedSelection{}, err
	}
	if err := authorizeComposition(ctx, b.deps.Authz, subj, "", sel.spec); err != nil {
		return nil, resolvedSelection{}, err
	}
	if err := b.resolver.checkReferencePolicies(ctx, nil, []string{referrer}, sel.spec, true); err != nil {
		return nil, resolvedSelection{}, err
	}
	return lease.startSpan(id.RunID, id.SessionID, id.SubjectID, id.Agents), sel, nil
}

// requireEngine refuses a run when no AI engine is configured. It is shared by
// every entry point so the refusal reads the same from Ask and Approve.
func (b *AgentBiz) requireEngine() error {
	if b.deps.Engine == nil {
		return errno.ErrNotConfigured.WithMessage("AI engine is not configured")
	}
	return nil
}

// resumableEngine is what Approve needs before it can answer a pause: an engine
// that can continue a suspended run, and the checkpoint store the suspension
// lives in.
func (b *AgentBiz) resumableEngine() (agentapi.Resumable, error) {
	if err := b.requireEngine(); err != nil {
		return nil, err
	}
	resumable, ok := b.deps.Engine.(agentapi.Resumable)
	if !ok {
		return nil, errno.ErrNotConfigured.WithMessage("the configured AI engine does not support approval")
	}
	if b.deps.Checkpoints == nil {
		return nil, errno.ErrNotConfigured.WithMessage("approval is not configured: no checkpoint store")
	}
	return resumable, nil
}
