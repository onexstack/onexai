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

package handler

import (
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
)

// TestSecretToProtoHidesTheKeyExceptOnCreate pins the redaction boundary. The
// key is client-supplied, so the create response echoes it once for
// confirmation; every other read (get, list, update) must leave the field empty
// rather than hand a stored credential to anyone who can list secrets.
func TestSecretToProtoHidesTheKeyExceptOnCreate(t *testing.T) {
	stored := &model.SecretM{
		SecretID:    "ak-chat",
		SecretKey:   "s3cr3t-value",
		Description: "chat service",
		CreatedAt:   1,
		UpdatedAt:   2,
	}

	create := conversion.SecretModelToSecretV1(stored, true)
	if create.SecretKey != "s3cr3t-value" {
		t.Errorf("create response secretKey = %q, want the key echoed once", create.SecretKey)
	}

	for _, tc := range []struct {
		name string
		got  string
	}{
		{"get", conversion.SecretModelToSecretV1(stored, false).SecretKey},
		{"list/update", conversion.SecretModelToSecretV1(stored, false).SecretKey},
	} {
		if tc.got != "" {
			t.Errorf("%s response leaked secretKey %q, want empty", tc.name, tc.got)
		}
	}

	// The non-secret fields must survive redaction; a hidden key that also hid
	// the description would be a regression, not a hardening.
	if create.SecretId != "ak-chat" || create.Description != "chat service" {
		t.Errorf("redaction dropped metadata: %+v", create)
	}
}
