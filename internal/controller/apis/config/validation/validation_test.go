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

package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/onexstack/onexai/internal/controller/apis/config"
	"github.com/onexstack/onexai/pkg/readmodel"
)

// TestValidatePromptAggregationKey pins the AES key-size rule: empty keeps
// legacy plaintext credentials usable, and any non-empty value must be one of
// the three sizes AES accepts.
func TestValidatePromptAggregationKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{name: "empty disables sealed credentials"},
		{name: "AES-128", key: "0123456789abcdef"},
		{name: "AES-192", key: "0123456789abcdef01234567"},
		{name: "AES-256", key: "0123456789abcdef0123456789abcdef"},
		{name: "not a key size", key: "0123456789abcdef01234", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validatePromptAggregation(
				&config.PromptAggregationConfiguration{ModelAPIKeyEncryptionKey: tt.key},
				field.NewPath("promptAggregation"),
			)
			assert.Equal(t, tt.wantErr, len(errs) > 0, "errs: %v", errs)
		})
	}
}

// TestValidateMirrorSchema pins the schema rule to the one the writer enforces.
// The component default is `mirror`, so a stricter rule here rejects the
// configuration the component ships with before any controller starts.
func TestValidateMirrorSchema(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr bool
	}{
		{name: "default schema", schema: readmodel.DefaultSchema},
		{name: "empty falls back to the default", schema: ""},
		{name: "identifier with digits", schema: "mirror_2"},
		{name: "hyphen is not an identifier", schema: "mirror-schema", wantErr: true},
		{name: "quote ends the identifier", schema: `onexai"; DROP SCHEMA "`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateMirror(&config.MirrorConfiguration{Schema: tt.schema}, field.NewPath("mirror"))
			assert.Equal(t, tt.wantErr, len(errs) > 0, "errs: %v", errs)
		})
	}
}
