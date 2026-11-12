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

package agentcli

import (
	"strings"
)

// Secrets travel through this package on their way to a process: an API key in
// the environment, a token on the command line. Both end up in places that
// outlive the run — a log line, an error message, the process table — so the
// argv and environment are redacted before anything writes them down.
//
// Redaction here is by name, not by value: this package cannot know which
// strings are secret, but it does know which flags and variables are *for*
// secrets, and those are the ones whose values have to go.

// secretFlags are the flags whose value is a secret. The value is the following
// argv element, or the part after "=" for the --flag=value form.
var secretFlags = map[string]struct{}{
	"--api-key":         {},
	"--apikey":          {},
	"--api_token":       {},
	"--auth-token":      {},
	"--token":           {},
	"--password":        {},
	"--secret":          {},
	"--client-secret":   {},
	"--access-token":    {},
	"--refresh-token":   {},
	"--oauth-token":     {},
	"--session-token":   {},
	"--authorization":   {},
	"--bedrock-api-key": {},
}

// secretNameRelevant reports whether a name (an environment variable, or a flag
// with its dashes removed) looks like it holds a secret.
func secretNameRelevant(name string) bool {
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), "-_"))
	if name == "" {
		return false
	}
	for _, marker := range []string{"secret", "token", "password", "passwd", "api_key", "apikey", "credential", "private_key"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// RedactArgs returns a copy of argv safe to log: the values of secret-bearing
// flags are replaced, everything else is left alone.
//
// It errs towards replacing too much. An argument that merely looks like a
// secret assignment is replaced too, because the cost of that is a less readable
// log line and the cost of the opposite is a live credential in a log file.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		out[i] = arg

		name, _, hasValue := strings.Cut(arg, "=")
		_, isSecretFlag := secretFlags[strings.ToLower(name)]
		if !isSecretFlag && !secretNameRelevant(name) {
			continue
		}

		if hasValue {
			out[i] = name + "=***"
			continue
		}
		// --flag value: the secret is the next element.
		if i+1 < len(args) {
			out[i+1] = "***"
			i++
		}
	}
	return out
}

// RedactEnv returns a copy of an environment slice safe to log: the value of any
// variable whose name looks like it holds a secret is replaced.
//
// A variable with no "=" is left as it is: it names a variable to pass through
// from the current environment, and the value is not in this slice at all.
func RedactEnv(env []string) []string {
	out := make([]string, len(env))
	for i, kv := range env {
		name, _, hasValue := strings.Cut(kv, "=")
		if hasValue && secretNameRelevant(name) {
			out[i] = name + "=***"
			continue
		}
		out[i] = kv
	}
	return out
}
