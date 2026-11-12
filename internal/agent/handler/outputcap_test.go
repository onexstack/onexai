package handler

import (
	"context"
	"testing"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// TestOutputCapsCeiling pins which caller gets which ceiling. The failure this
// guards against is silent: a caller that matches no entry must land on
// "default" rather than on 0, because 0 means "no ceiling" and would leave the
// surface with the largest bill uncapped.
func TestOutputCapsCeiling(t *testing.T) {
	caps := &OutputCaps{
		BySurface:   map[string]int{DefaultSurface: 8192, "lark": 2048, "onex": 16384},
		SurfaceKeys: map[string]string{"oai-chat-apiserver": "lark", "onex-agent-apiserver": "onex"},
	}

	tests := []struct {
		name   string
		caller string
		want   int
	}{
		{name: "the Lark bot is capped short", caller: "oai-chat-apiserver", want: 2048},
		{name: "the console and comments are not", caller: "onex-agent-apiserver", want: 16384},
		{name: "an unknown caller gets the default", caller: "some-new-service", want: 8192},
		{name: "a caller that is not a service gets the default", caller: "", want: 8192},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := caps.Ceiling(tc.caller); got != tc.want {
				t.Fatalf("Ceiling(%q) = %d, want %d", tc.caller, got, tc.want)
			}
		})
	}
}

// TestOutputCapsCeilingWhenUnconfigured pins the two empty cases, which both
// have to mean "we have no ceiling of our own": a nil OutputCaps (no
// agent.max-output-tokens at all) and one whose maps are empty. Returning
// anything else here would cap runs at a number nobody configured.
func TestOutputCapsCeilingWhenUnconfigured(t *testing.T) {
	var none *OutputCaps
	if got := none.Ceiling("oai-chat-apiserver"); got != 0 {
		t.Fatalf("a nil OutputCaps must not impose a ceiling, got %d", got)
	}

	empty := &OutputCaps{}
	if got := empty.Ceiling("oai-chat-apiserver"); got != 0 {
		t.Fatalf("an empty OutputCaps must not impose a ceiling, got %d", got)
	}

	// A surface name that the ceilings do not declare resolves to the default,
	// which is the state the startup validation refuses to let a deployment
	// reach — but resolving it here means a hand-edited database or a config that
	// skipped validation still cannot make a caller uncapped.
	unmatched := &OutputCaps{
		BySurface:   map[string]int{DefaultSurface: 8192},
		SurfaceKeys: map[string]string{"oai-chat-apiserver": "lark"},
	}
	if got := unmatched.Ceiling("oai-chat-apiserver"); got != 8192 {
		t.Fatalf("an undeclared surface must fall back to the default, got %d", got)
	}
}

// TestCallerKeyReadsTheServiceNotTheUser pins where a surface's identity comes
// from. The agent flow overwrites the request's authorization subject with the
// end user once a run starts, so the ceiling has to be resolved from the subject
// as it stands at the edge — the service that signed the request.
func TestCallerKeyReadsTheServiceNotTheUser(t *testing.T) {
	service := authz.ContextWithSubject(context.Background(), authz.SubjectFromService("oai-chat-apiserver"))
	if got := callerKey(service); got != "oai-chat-apiserver" {
		t.Fatalf("callerKey = %q, want the service name", got)
	}

	user := authz.ContextWithSubject(context.Background(), authz.Subject{
		Kind:     authz.SubjectKindUser,
		UserID:   "u-123",
		UserName: "someone",
	})
	if got := callerKey(user); got != "" {
		t.Fatalf("callerKey = %q, want empty for a non-service subject", got)
	}

	if got := callerKey(context.Background()); got != "" {
		t.Fatalf("callerKey = %q, want empty when the context carries no subject", got)
	}
}
