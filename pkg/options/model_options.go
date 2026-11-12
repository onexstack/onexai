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

package options

import (
	"fmt"

	"github.com/spf13/pflag"
)

// ModelOptions configures how model endpoints are stored.
type ModelOptions struct {
	// APIKeyEncryptionKey is the AES key used to encrypt a model endpoint's
	// API key at rest. It must be 16, 24 or 32 bytes. Empty means user-owned
	// endpoints cannot be saved: a user's provider key is never written in
	// plaintext. Platform-owned endpoints written before this option existed
	// remain readable because OpenAPIKey treats a value without the cipher
	// prefix as legacy plaintext.
	APIKeyEncryptionKey string `json:"api-key-encryption-key" mapstructure:"api-key-encryption-key"`
}

// NewModelOptions creates a ModelOptions with default values.
func NewModelOptions() *ModelOptions {
	return &ModelOptions{}
}

// Validate checks the ModelOptions fields.
func (o *ModelOptions) Validate() []error {
	if o.APIKeyEncryptionKey == "" {
		return nil
	}
	switch len(o.APIKeyEncryptionKey) {
	case 16, 24, 32:
		return nil
	default:
		return []error{fmt.Errorf("model.api-key-encryption-key must be 16, 24 or 32 bytes, got %d",
			len(o.APIKeyEncryptionKey))}
	}
}

// AddFlags adds model-related flags to the given FlagSet.
func (o *ModelOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.APIKeyEncryptionKey, fullPrefix+".api-key-encryption-key", o.APIKeyEncryptionKey,
		"AES key (16/24/32 bytes) used to encrypt model endpoint API keys at rest.")
}
