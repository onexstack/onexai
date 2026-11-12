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
	"errors"
	"fmt"
	"log/slog"

	"github.com/onexstack/onexai/internal/agent/model"
)

// Model resolution errors. They are sentinels so the HTTP handler can map them
// to a status the caller can act on rather than a generic 500.
var (
	// ErrModelDisabled reports that a configured endpoint exists but is turned
	// off. It is not silently replaced by the platform key: the user asked for
	// their own endpoint, and spending platform money instead is a different
	// decision.
	ErrModelDisabled = errors.New("the configured model endpoint is disabled")
	// ErrModelForbidden reports that the endpoint belongs to another subject.
	ErrModelForbidden = errors.New("the configured model endpoint belongs to another subject")
	// ErrModelUnavailable reports that the endpoint exists but could not be
	// turned into a client.
	ErrModelUnavailable = errors.New("the configured model endpoint is unavailable")
)

// requestedModel is an explicitly named endpoint after the lookups that have to
// happen before the quota gate.
//
// Carrying the answer is what lets the second phase learn whether the run
// spends platform tokens without reading the same record again — and it is why
// a name that resolved is never looked up twice: a row whose Name differs from
// the key it is stored under would miss on the second read and silently fall
// back to the deployment default.
type requestedModel struct {
	// raw is what the caller named. Kept for the fallback warning only.
	raw string
	// name and owned are the canonical row name and whether the caller owns it.
	name  string
	owned bool
	// found reports whether the name resolved to a usable row.
	found bool
}

// resolveRequestedModel validates an explicitly requested endpoint before the
// quota checks, because a user-owned endpoint does not spend the platform's
// token budget. A name that resolves to nothing is treated as "not configured"
// — OneX sends the deterministic per-user name on every run, and a 404 is the
// normal state before the user has configured anything.
// strict makes a named-but-unusable endpoint a request error instead of a
// silent fallback. Composed debug runs set it: the user picked that model from
// a menu, and running on a different one would make the result describe
// something nobody selected. Ordinary callers keep the fallback because OneX
// sends a deterministic per-user name before that user has configured one.
func (b *AgentBiz) resolveRequestedModel(
	ctx context.Context, requested, subjectID string, strict bool,
) (requestedModel, error) {
	if requested == "" {
		return requestedModel{}, nil
	}
	rm := requestedModel{raw: requested}
	row, ok := b.modelRow(ctx, requested)
	if !ok {
		if strict {
			return requestedModel{}, &CompositionError{Rejected: []string{
				fmt.Sprintf("model %q (missing)", requested),
			}}
		}
		return rm, nil
	}
	if !row.Enabled() {
		if strict {
			return requestedModel{}, &CompositionError{Rejected: []string{
				fmt.Sprintf("model %q (disabled)", requested),
			}}
		}
		return requestedModel{}, ErrModelDisabled
	}
	if row.Owner != "" && row.Owner != subjectID {
		return requestedModel{}, ErrModelForbidden
	}
	rm.name, rm.owned, rm.found = model.CanonicalRef(row.Namespace, row.Name), row.Owner != "", true
	return rm, nil
}

// modelRow is the one endpoint lookup. Deps.Resources is documented as nil-able
// (see the field's comment): a deployment without a resource backend has no
// configured endpoints, which is the same answer as a miss and must not be a
// panic.
func (b *AgentBiz) modelRow(ctx context.Context, name string) (*model.ModelM, bool) {
	if b.deps.Resources == nil || name == "" {
		return nil, false
	}
	row, err := b.deps.Resources.Models().Get(ctx, model.EntryFilter(name))
	if err != nil {
		return nil, false
	}
	return row, row != nil
}

// resolveRunModel resolves the effective endpoint for a run:
//
//  1. the requested endpoint, when it exists and is usable;
//  2. otherwise the first non-empty model declared by the selected agents;
//  3. otherwise empty, which means the deployment default.
//
// A requested name that resolves to nothing falls through (the user has not
// configured a model); a name that exists but is disabled or owned by another
// subject is an error, because silently spending the platform key instead is
// not what the caller asked for.
func (b *AgentBiz) resolveRunModel(ctx context.Context, requested requestedModel, sel resolvedSelection, subjectID string) (string, bool, error) {
	if requested.found {
		return requested.name, requested.owned, nil
	}
	if requested.raw != "" {
		slog.WarnContext(ctx, "requested model endpoint not found; falling back to the agent/default",
			"model", requested.raw)
	}

	for _, m := range sel.models {
		row, ok := b.modelRow(ctx, m.name)
		if !ok {
			slog.WarnContext(ctx, "agent model endpoint not found; falling back",
				"agent", m.agent, "model", m.name)
			continue
		}
		if !row.Enabled() {
			slog.WarnContext(ctx, "agent model endpoint is disabled; falling back",
				"agent", m.agent, "model", m.name)
			continue
		}
		if row.Owner != "" && row.Owner != subjectID {
			return "", false, ErrModelForbidden
		}
		return model.CanonicalRef(row.Namespace, row.Name), row.Owner != "", nil
	}

	return "", false, nil
}
