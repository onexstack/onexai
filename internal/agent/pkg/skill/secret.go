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

package skill

import (
	"context"
	"fmt"
	"strings"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
)

// SecretLookup reads a named secret. It is deliberately narrower than the
// resource store's own interface: a skill provider needs to resolve one
// credential and has no business listing, creating or deleting secrets.
// store.SecretStore satisfies it structurally.
type SecretLookup interface {
	Get(ctx context.Context, opts *where.Options) (*model.SecretM, error)
}

// resolveToken returns the credential a skill record authenticates with.
//
// A record may name a secret ("token_secret_id"), carry the value inline
// ("token"), or do both; the named secret wins. The inline form is kept for
// records written before secrets were a first-class resource, and is the one
// thing an operator should migrate away from — it puts the credential in the
// same table as the skill's description.
//
// The resolved value is never logged, and an unresolved secret id is an error
// rather than a silent empty token: a private repository fetched without
// credentials fails with a 404 that reads exactly like a wrong URL.
func resolveToken(ctx context.Context, lookup SecretLookup, secretID, inline string) (string, error) {
	if strings.TrimSpace(secretID) == "" {
		return inline, nil
	}
	if lookup == nil {
		return "", fmt.Errorf("skill: secret %q is referenced but no secret store is configured", secretID)
	}
	secret, err := lookup.Get(ctx, where.F("secret_id", secretID))
	if err != nil || secret == nil {
		return "", fmt.Errorf("skill: secret %q not found", secretID)
	}
	if secret.SecretKey == "" {
		return "", fmt.Errorf("skill: secret %q is empty", secretID)
	}
	return secret.SecretKey, nil
}
