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

package webhook

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

const (
	// managedTokenSecretIDPrefix/Suffix make a hook's managed credential
	// recognisable without consulting the store: the ID is derived from the
	// hook name, so rotating a token reuses the same Secret instead of
	// accumulating orphans.
	managedTokenSecretIDPrefix = "webhook-"
	managedTokenSecretIDSuffix = "-token"

	// managedTokenDescriptionPrefix marks the row even in deployments whose
	// secret store has no kind label (the SQL fallback). Cleanup checks it
	// before deleting anything.
	managedTokenDescriptionPrefix = "onexai-managed webhook token:"
)

// managedTokenSecretID returns the deterministic SecretID that holds one hook's
// credential. SecretID is capped at 128 bytes by the model; a name long enough
// to overflow it is replaced by a hash that is still derived from the name.
func managedTokenSecretID(name string) string {
	id := managedTokenSecretIDPrefix + name + managedTokenSecretIDSuffix
	if len(id) <= 128 {
		return id
	}
	sum := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%s%x%s", managedTokenSecretIDPrefix, sum[:16], managedTokenSecretIDSuffix)
}

func managedTokenDescription(name string) string {
	return managedTokenDescriptionPrefix + " " + name
}

func isManagedTokenSecret(row *model.SecretM) bool {
	if row == nil {
		return false
	}
	return row.Kind == model.SecretKindWebhook ||
		strings.HasPrefix(row.Description, managedTokenDescriptionPrefix)
}

// materializeToken moves a write-only plaintext credential into the secret
// store and replaces it with a reference on the hook.
//
// It runs after validation and after the duplicate-name check, so a rejected
// write never leaves a credential behind. A token that is already stored under
// the managed ID is updated in place, which is what makes rotation keep the
// same reference.
func (s *Service) materializeToken(ctx context.Context, hook *model.WebhookM) error {
	token := strings.TrimSpace(hook.AuthToken)
	hook.AuthToken = ""
	if token == "" {
		return s.validateReferencedSecret(ctx, hook)
	}
	if s.secrets == nil {
		return ErrSecretStoreNotConfigured
	}

	id := managedTokenSecretID(hook.Name)
	row, err := s.secrets.Get(ctx, where.F("secret_id", id))
	if err != nil && !errors.Is(err, agentstore.ErrNotFound) {
		return err
	}
	if row != nil && !isManagedTokenSecret(row) {
		// Somebody else owns this ID. Overwriting it would rotate a credential
		// this hook never created, so refuse rather than surprise the owner.
		return &ValidationError{
			Field:   "authToken",
			Message: "a secret with the webhook's managed name already exists and was not created by it",
		}
	}

	description := managedTokenDescription(hook.Name)
	if row == nil {
		row = &model.SecretM{
			SecretID:    id,
			SecretKey:   token,
			Description: description,
			Kind:        model.SecretKindWebhook,
		}
		if err := s.secrets.Create(ctx, row); err != nil {
			return err
		}
	} else {
		row.SecretKey = token
		row.Description = description
		row.Kind = model.SecretKindWebhook
		if err := s.secrets.Update(ctx, row); err != nil {
			return err
		}
	}

	hook.SecretRef = id
	return nil
}

// validateReferencedSecret rejects a manual reference to another hook's managed
// token. A hook may still point at an ordinary credential, and at its own
// managed token (the console shows that state when editing).
func (s *Service) validateReferencedSecret(ctx context.Context, hook *model.WebhookM) error {
	if hook.SecretRef == "" || s.secrets == nil {
		return nil
	}
	if hook.SecretRef == managedTokenSecretID(hook.Name) {
		return nil
	}
	row, err := s.secrets.Get(ctx, where.F("secret_id", hook.SecretRef))
	if err != nil {
		if errors.Is(err, agentstore.ErrNotFound) {
			return nil
		}
		return err
	}
	if row != nil && row.Kind == model.SecretKindWebhook {
		return &ValidationError{
			Field:   "secretRef",
			Message: "must reference a credential, not another webhook's managed token",
		}
	}
	return nil
}

// retireManagedToken deletes the managed credential an update or delete just
// stopped using. Only the hook's own managed Secret is eligible; a reference to
// a user-created credential is never touched.
func (s *Service) retireManagedToken(ctx context.Context, existing *model.WebhookM, nextSecretRef string) {
	if existing == nil || existing.SecretRef == "" {
		return
	}
	managedID := managedTokenSecretID(existing.Name)
	if existing.SecretRef != managedID || nextSecretRef == managedID {
		return
	}
	s.deleteManagedToken(ctx, managedID)
}

func (s *Service) deleteManagedToken(ctx context.Context, secretID string) {
	if s.secrets == nil {
		return
	}
	row, err := s.secrets.Get(ctx, where.F("secret_id", secretID))
	if err != nil {
		if !errors.Is(err, agentstore.ErrNotFound) {
			slog.WarnContext(ctx, "webhook: could not inspect the managed credential for cleanup",
				"secretID", secretID, "error", err)
		}
		return
	}
	if !isManagedTokenSecret(row) {
		return
	}
	if err := s.secrets.Delete(ctx, where.F("secret_id", secretID)); err != nil &&
		!errors.Is(err, agentstore.ErrNotFound) {
		// The hook write has already succeeded; a failed cleanup leaves an
		// orphan, not a broken hook.
		slog.WarnContext(ctx, "webhook: could not delete the managed credential",
			"secretID", secretID, "error", err)
	}
}
