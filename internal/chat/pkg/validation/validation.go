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

// Package validation holds the REST surface's request validation.
//
// It is the onexstack shape — a Validator the handlers receive through
// dependency injection — with one Validate<Type>Request method per request type.
// Validation is part of the endpoint contract, not a deployment switch.
package validation

import (
	"context"
	"strings"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agentref"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// Validator handles custom business validation logic.
// It holds dependencies required for deep validation, such as database access.
type Validator struct {
	// Some complex validation logic may require direct database queries.
	// This is just an example. If validation requires other dependencies
	// like clients, services, resources, etc., they can all be injected here.
	store store.IStore
}

// ProviderSet is the Wire provider set for the validation package.
var ProviderSet = wire.NewSet(New, wire.Bind(new(any), new(*Validator)))

// New creates and initializes a new Validator instance with the required dependencies.
func New(ds store.IStore) *Validator {
	return &Validator{store: ds}
}

// ValidateOncallGroup checks the on-call group metadata a create or update
// carries. It validates the same object both verbs bind, because the path is
// authoritative for the identity: the create names the group in the body and
// the update takes the name from the path, so in either case a blank name is
// what would produce a row nobody can address.
//
// # The agents column is a reference list, and is parsed as one
//
// It holds the same syntax a conversation's refs do — `@peer`, `@oncall`,
// `@default`, or a bare agent name — and the scheduler runs it through the same
// pipeline. It was a bare JSON column that accepted any string, and a bad entry
// there fails late and confusingly: `expand` reports an unknown kind or a
// malformed reference at message time, so the group answers every question with
// an error card and nothing points back at the typo. This is the only place the
// mistake is still cheap.
func (v *Validator) ValidateOncallGroup(_ context.Context, obj *model.OncallGroupM) error {
	if strings.TrimSpace(obj.Name) == "" {
		return errno.ErrInvalidArgument.WithMessage("name is required")
	}

	agents := obj.AgentList()
	if obj.Agents != "" && agents == nil {
		// AgentList swallows a parse failure into nil. That is the right call at
		// refresh time — see its doc — and the wrong one here: a value that
		// cannot be read back would create a group that silently resolves no
		// agents, and the operator would have no way to tell it from an empty
		// list.
		return errno.ErrInvalidArgument.WithMessage("agents: not a JSON array of agent references")
	}

	refs, err := agentref.ParseRefList(agents)
	if err != nil {
		return errno.ErrInvalidArgument.WithMessage("agents: %s", err.Error())
	}

	// A kind with no decider is refused here rather than at scheduling time:
	// `expand` errors on one, and an on-call group is reached by every
	// conversation that shares its ticket group, so the blast radius of a typo
	// is not the group — it is every ticket group bound to it.
	for _, r := range refs {
		if r.IsRef() && !agentref.KnownKind(r.Kind) {
			return errno.ErrInvalidArgument.WithMessage(
				"agents: %q names no known reference kind (known: %s)",
				r.String(), strings.Join(agentref.Kinds(), ", "))
		}
	}

	return nil
}
