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

package mcpclient

import (
	"regexp"
	"strings"
	"testing"
)

// legalToolName is the function-name rule shared by the OpenAI and Anthropic
// APIs. Every name this package hands to a model must match it.
var legalToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// The names the zke-kubeagent server actually exposes: dots are the
		// reason this function exists.
		{"dotted resource list", "kubernetes_resource.list", "kubernetes_resource_list"},
		{"dotted pod logs", "kubernetes_pod.logs", "kubernetes_pod_logs"},
		{"dotted metrics", "kubernetes_metrics.pods", "kubernetes_metrics_pods"},
		{"multiple dots", "a.b.c", "a_b_c"},
		// Names that are already legal pass through untouched.
		{"already legal", "inspect_analyze", "inspect_analyze"},
		{"dash kept", "scan-run", "scan-run"},
		{"digits kept", "tool2", "tool2"},
		{"trimmed", "  padded  ", "padded"},
		// Nothing usable survives.
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"dots only", "...", ""},
		{"dashes only", "---", ""},
		{"non ascii only", "工具", ""},
		// Truncation.
		{"truncated", strings.Repeat("x", 70), strings.Repeat("x", 64)},
		// The dot is sanitised first, so the 64th character is the underscore
		// and "y" falls off the end.
		{"truncated with dot", strings.Repeat("x", 63) + ".y", strings.Repeat("x", 63) + "_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeName(tt.in)
			if got != tt.want {
				t.Fatalf("SanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if got != "" && !legalToolName.MatchString(got) {
				t.Fatalf("SanitizeName(%q) = %q, which is not a legal function name", tt.in, got)
			}
		})
	}
}

func TestUniquify(t *testing.T) {
	t.Run("free name is kept", func(t *testing.T) {
		taken := map[string]bool{}
		if got := Uniquify("kubernetes_pod_logs", taken); got != "kubernetes_pod_logs" {
			t.Fatalf("got %q, want the name unchanged", got)
		}
		if !taken["kubernetes_pod_logs"] {
			t.Fatal("the chosen name must be recorded in taken")
		}
	})

	t.Run("collisions get suffixes", func(t *testing.T) {
		// A built-in tool is reserved, so an MCP tool of the same name cannot
		// shadow it.
		taken := map[string]bool{"current_time": true}
		for i, want := range []string{"current_time_2", "current_time_3", "current_time_4"} {
			if got := Uniquify("current_time", taken); got != want {
				t.Fatalf("call %d: got %q, want %q", i+1, got, want)
			}
		}
	})

	t.Run("suffix stays within the limit", func(t *testing.T) {
		base := strings.Repeat("z", MaxToolNameLen)
		taken := map[string]bool{base: true}
		got := Uniquify(base, taken)
		if got == base {
			t.Fatal("a taken name must be renamed")
		}
		if len(got) > MaxToolNameLen {
			t.Fatalf("got %q with length %d, want at most %d", got, len(got), MaxToolNameLen)
		}
		if !legalToolName.MatchString(got) {
			t.Fatalf("got %q, which is not a legal function name", got)
		}
	})

	t.Run("empty base stays empty", func(t *testing.T) {
		if got := Uniquify("", map[string]bool{}); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}
