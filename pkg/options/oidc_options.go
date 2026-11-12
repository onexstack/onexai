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

import "time"

// OIDCOptions configures the platform's OIDC-compatible token issuer.
//
// It is an extension of the login response, not a replacement for it: the
// existing HS256 token stays the browser credential while a deployment moves
// its machine-to-machine surface onto the Kubernetes authentication model. A
// consumer validates the OIDC token through the discovery document and JWKS
// this service publishes.
type OIDCOptions struct {
	// Enabled turns on token issuance and the discovery/JWKS endpoints.
	Enabled bool `json:"enabled" mapstructure:"enabled"`

	// IssuerURL is the value of the `iss` claim and the issuer in the discovery
	// document. It must be the URL a validator can reach this service at.
	IssuerURL string `json:"issuer-url" mapstructure:"issuer-url"`

	// Audiences are the acceptable `aud` values.
	Audiences []string `json:"audiences" mapstructure:"audiences"`

	// KeyFile is an RSA private key in PEM form. Empty generates an ephemeral
	// key, which is useful for local development and invalidates every token on
	// restart.
	KeyFile string `json:"key-file" mapstructure:"key-file"`

	// KeyID identifies the signing key in the JWKS. Empty derives one from the
	// public key.
	KeyID string `json:"key-id" mapstructure:"key-id"`

	// TokenTTL bounds an issued token. Empty means one hour.
	TokenTTL time.Duration `json:"token-ttl" mapstructure:"token-ttl"`
}
