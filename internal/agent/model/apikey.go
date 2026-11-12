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

package model

import (
	"errors"
	"strings"

	secretutil "github.com/onexstack/onexstack/pkg/util/secret"
)

// apiKeyCipherPrefix marks a value encrypted by SealAPIKey. A value without it
// is legacy plaintext and is returned unchanged by OpenAPIKey, which is what
// keeps platform endpoints written before encryption existed readable.
const apiKeyCipherPrefix = "enc:v1:"

// ErrAPIKeyKeyMissing reports that an encrypted key exists but no encryption
// key was configured. It is an operator error, not a caller error: the value
// cannot be recovered without the key it was sealed with.
var ErrAPIKeyKeyMissing = errors.New("model: no api-key encryption key configured")

// IsSealedAPIKey reports whether value carries the storage-only cipher prefix.
//
// Such a value is not a usable credential: it can only be meaningfully consumed
// by OpenAPIKey with the deployment key that produced it. Callers use this to
// reject a client that submitted a stored value, and to make sure they never
// forward one to an upstream provider.
func IsSealedAPIKey(value string) bool {
	return strings.HasPrefix(value, apiKeyCipherPrefix)
}

// SealAPIKey encrypts a plaintext API key for storage.
//
// An empty plaintext is returned as an empty string. A value already carrying
// the cipher prefix is returned unchanged so a read-modify-write that echoes a
// sealed value cannot double-encrypt it.
func SealAPIKey(plaintext, key string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if strings.HasPrefix(plaintext, apiKeyCipherPrefix) {
		return plaintext, nil
	}
	if key == "" {
		return "", ErrAPIKeyKeyMissing
	}
	ciphertext, err := secretutil.Encrypt(plaintext, secretutil.WithKey(key))
	if err != nil {
		return "", err
	}
	return apiKeyCipherPrefix + ciphertext, nil
}

// OpenAPIKey decrypts a stored API key.
//
// A value without the cipher prefix is legacy plaintext and is returned as
// stored. An empty value is returned as an empty string.
func OpenAPIKey(stored, key string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, apiKeyCipherPrefix) {
		return stored, nil
	}
	if key == "" {
		return "", ErrAPIKeyKeyMissing
	}
	return secretutil.Decrypt(strings.TrimPrefix(stored, apiKeyCipherPrefix), secretutil.WithKey(key))
}
