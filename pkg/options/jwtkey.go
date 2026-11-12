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
	"strings"
)

// builtinJWTSecrets are signing-key values that ship with the code or with the
// sample configuration. They are known to the whole world, so a token signed
// with one is forgeable by anyone; accepting one is the same as having no
// authentication. "CHANGE_ME" is the code default of the JWTOptions struct,
// which is what a config that omits jwt.key silently falls back to.
var builtinJWTSecrets = map[string]struct{}{
	"change-me-in-production": {},
	"CHANGE_ME":               {},
	// "oai-dev-jwt-key-7b3d9e" shipped in configs/oai-{chat,agent}-apiserver.dev.yaml
	// and was committed to git. chat signs the token, agent validates it on
	// /v1/admin, so anyone with repo read access can mint an is_admin=true token
	// that the management surface accepts. A deployment still on this value must
	// fail closed and name a fresh key rather than start.
	"oai-dev-jwt-key-7b3d9e": {},
}

// ValidateJWTKey rejects an empty or built-in placeholder JWT signing key. A
// deployment must name an explicit key (in the config file or the environment);
// there is no usable default.
func ValidateJWTKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("jwt.key is empty: an explicit JWT signing key must be configured")
	}
	if _, placeholder := builtinJWTSecrets[trimmed]; placeholder {
		return fmt.Errorf("jwt.key is the built-in placeholder %q: configure an explicit JWT signing key", trimmed)
	}
	return nil
}
