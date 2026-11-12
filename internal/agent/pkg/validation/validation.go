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

// Package validation holds the API's request validation.
//
// It follows the onexstack shape: a Validator that the handlers receive through
// dependency injection, with one Validate<Type>Request method per request type
// and a per-resource rule set. Validation is part of the endpoint contract, not
// a deployment switch.
package validation

import (
	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/agent/store"
)

// Validator carries whatever the rules need to look up. Today that is only the
// resource store; a rule that has to read a record before deciding (a uniqueness
// check, say) reaches it through here, which is why the store is a dependency
// of the validator rather than of the rule functions.
type Validator struct {
	resources store.AIStore
}

// ProviderSet is the Wire provider set for the validation package.
var ProviderSet = wire.NewSet(New, wire.Bind(new(any), new(*Validator)))

// New creates a Validator.
func New(resources store.AIStore) *Validator {
	return &Validator{resources: resources}
}
