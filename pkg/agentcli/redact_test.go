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
	"testing"
)

func TestRedactArgsStripsSecrets(t *testing.T) {
	args := []string{
		"claude",
		"-p", "prompt",
		"--api-key", "sk-live-abcdef",
		"--verbose",
		"--model", "opus",
		"--token=ghp_secret",
		"--append-system-prompt", "be terse",
	}
	got := RedactArgs(args)

	want := []string{
		"claude",
		"-p", "prompt",
		"--api-key", "***",
		"--verbose",
		"--model", "opus",
		"--token=***",
		"--append-system-prompt", "be terse",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d arguments, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argument %d = %q, want %q", i, got[i], want[i])
		}
	}

	// The originals are not touched: redaction is what gets logged, and a copy
	// that shares the caller's array would corrupt the command being run.
	if args[4] != "sk-live-abcdef" {
		t.Errorf("RedactArgs modified its input: %q", args[4])
	}
}

func TestRedactArgsOnASecretShapedName(t *testing.T) {
	// Redaction is by name, because a value cannot be recognised. Anything whose
	// name looks like it holds a secret is masked, including forms this package
	// has never seen.
	got := RedactArgs([]string{"env", "MY_API_KEY=live", "GITHUB_TOKEN=ghp_x", "LANG=en_US.UTF-8", "PLAIN=1"})

	for i, want := range []string{"env", "MY_API_KEY=***", "GITHUB_TOKEN=***", "LANG=en_US.UTF-8", "PLAIN=1"} {
		if got[i] != want {
			t.Errorf("argument %d = %q, want %q", i, got[i], want)
		}
	}

	// A trailing secret flag with no value must not panic or reach past the end.
	tail := RedactArgs([]string{"claude", "--api-key"})
	if len(tail) != 2 || tail[1] != "--api-key" {
		t.Errorf("a trailing --api-key with no value = %q, want it left alone", tail)
	}
}

func TestRedactEnvMasksSecrets(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"LARK_APP_SECRET=live-secret",
		"HOME=/root",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-value",
		"AWS_SECRET_ACCESS_KEY=aws-value",
		"LANG=en_US.UTF-8",
		"PASSTHROUGH",
	}
	got := RedactEnv(env)

	want := []string{
		"PATH=/usr/bin",
		"LARK_APP_SECRET=***",
		"HOME=/root",
		"CLAUDE_CODE_OAUTH_TOKEN=***",
		"AWS_SECRET_ACCESS_KEY=***",
		"LANG=en_US.UTF-8",
		// A bare name is not an assignment: it means "pass this variable through
		// from the current environment", and the value is not in this slice.
		"PASSTHROUGH",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("variable %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, kv := range got {
		if strings.Contains(kv, "live-secret") || strings.Contains(kv, "oauth-value") || strings.Contains(kv, "aws-value") {
			t.Errorf("a secret survived redaction: %q", kv)
		}
	}
}
