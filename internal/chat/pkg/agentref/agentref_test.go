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

package agentref

import (
	"errors"
	"strings"
	"testing"
)

// TestParseRef pins what each stored shape parses to. The package is the only
// place the `@` syntax is read — the scheduler and the /agent command both go
// through it — so a change here changes what a stored declaration means, and
// these cases are the definition rather than an illustration.
func TestParseRef(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Ref
	}{
		{
			name: "a bare name is a literal, not a degenerate reference",
			in:   "onex-u-user-a1b2c3",
			want: Ref{Literal: "onex-u-user-a1b2c3"},
		},
		{
			name: "a reference with no argument",
			in:   "@peer",
			want: Ref{Kind: KindPeer},
		},
		{
			name: "a reference with an argument",
			in:   "@oncall:pay",
			want: Ref{Kind: KindOncall, Arg: "pay"},
		},
		{
			name: "only the first colon separates, so an argument may contain one",
			in:   "@oncall:pay:sub",
			want: Ref{Kind: KindOncall, Arg: "pay:sub"},
		},
		{
			name: "surrounding whitespace is trimmed before anything is read",
			in:   "  @peer  ",
			want: Ref{Kind: KindPeer},
		},
		{
			name: "the whitespace around the argument is trimmed too",
			in:   "@oncall: pay ",
			want: Ref{Kind: KindOncall, Arg: "pay"},
		},
		{
			name: "a kind nobody implements still parses: the vocabulary is the caller's check",
			in:   "@nope",
			want: Ref{Kind: "nope"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRef(tt.in)
			if err != nil {
				t.Fatalf("ParseRef(%q) = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseRef(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseRefErrors pins the two refusals apart. They are sentinel values so a
// caller can word the reply differently — "you typed nothing" and "you typed the
// sigil and stopped" are different sentences — which only works if the errors
// stay distinguishable.
func TestParseRefErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrEmptyRef},
		{"only whitespace", "   ", ErrEmptyRef},
		{"the sigil alone", "@", ErrMalformedRef},
		{"the sigil and whitespace", "@   ", ErrMalformedRef},
		{"a colon before any kind", "@:peer", ErrMalformedRef},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRef(tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ParseRef(%q) error = %v, want it to wrap %v", tt.in, err, tt.want)
			}
			if got != (Ref{}) {
				t.Errorf("ParseRef(%q) returned %+v alongside an error", tt.in, got)
			}
		})
	}
}

// TestRefStringRoundTrips pins that the rendering is the inverse of the parse.
//
// It is not cosmetic: the rendered form is what /agent writes into
// session.refs, so a reference that does not survive the round trip is one the
// scheduler reads differently from the way it was written.
func TestRefStringRoundTrips(t *testing.T) {
	for _, in := range []string{"onex-u-user-a1b2c3", "@peer", "@oncall:pay"} {
		ref, err := ParseRef(in)
		if err != nil {
			t.Fatalf("ParseRef(%q) = %v", in, err)
		}
		if got := ref.String(); got != in {
			t.Errorf("ParseRef(%q).String() = %q, want the input back", in, got)
		}
		again, err := ParseRef(ref.String())
		if err != nil {
			t.Fatalf("re-parsing %q = %v", ref.String(), err)
		}
		if again != ref {
			t.Errorf("re-parsing %q gave %+v, want %+v", ref.String(), again, ref)
		}
	}
}

// TestRefIsRef pins the one predicate every consumer uses to branch: whether an
// entry names an agent or needs resolving.
func TestRefIsRef(t *testing.T) {
	if (Ref{Kind: KindPeer}).IsRef() != true {
		t.Error("a kind-bearing Ref does not report itself as a reference")
	}
	if (Ref{Literal: "ops-agent"}).IsRef() != false {
		t.Error("a literal Ref reports itself as a reference")
	}
}

// TestValidateAgentName pins the two names the reference syntax would shadow or
// split. It is the rule that keeps the literal space and the reference space
// disjoint, so a name it lets through is one ParseRef will read as something
// else.
func TestValidateAgentName(t *testing.T) {
	valid := []string{"ops-agent", "onex-u-user-a1b2c3", "onex-x-max-tier", "a"}
	for _, name := range valid {
		if err := ValidateAgentName(name); err != nil {
			t.Errorf("ValidateAgentName(%q) = %v, want it accepted", name, err)
		}
	}

	invalid := map[string]string{
		"":        "empty",
		"@peer":   "reference syntax",
		"@":       "reference syntax",
		"a.b":     "must not contain",
		"ops.ag":  "must not contain",
		"a . b":   "must not contain",
		"@oncall": "reference syntax",
	}
	for name, want := range invalid {
		err := ValidateAgentName(name)
		if err == nil {
			t.Errorf("ValidateAgentName(%q) was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateAgentName(%q) error = %q, want it to contain %q", name, err, want)
		}
	}
}

// TestParseRefList pins that a bad entry is refused and named by position. The
// message is shown to whoever typed the list, and "entry 2" is actionable where
// "invalid reference" is not.
func TestParseRefList(t *testing.T) {
	got, err := ParseRefList([]string{"ops-agent", "@peer", "@oncall:pay"})
	if err != nil {
		t.Fatalf("ParseRefList() = %v", err)
	}
	if len(got) != 3 || got[1].Kind != KindPeer || got[2].Arg != "pay" {
		t.Errorf("ParseRefList() = %+v, want the three entries parsed in order", got)
	}

	if _, err := ParseRefList([]string{"ops-agent", "@"}); err == nil {
		t.Fatal("ParseRefList() accepted a malformed entry")
	} else if !strings.Contains(err.Error(), "entry 2") {
		t.Errorf("error = %q, want it to name the offending position", err)
	} else if !errors.Is(err, ErrMalformedRef) {
		t.Errorf("error = %v, want it to wrap ErrMalformedRef", err)
	}
}

// TestKnownKind pins the vocabulary the decider registry is checked against.
//
// The two must agree, and the registry panics at init when they do not; this
// asserts the other direction — that a kind the registry implements is one the
// vocabulary admits, so /agent does not refuse a reference that would have
// worked.
func TestKnownKind(t *testing.T) {
	for _, k := range Kinds() {
		if !KnownKind(k) {
			t.Errorf("Kinds() lists %q but KnownKind(%q) is false", k, k)
		}
	}
	for _, k := range []string{"", "nope", "Peer", "@peer", "oncall "} {
		if KnownKind(k) {
			t.Errorf("KnownKind(%q) = true, want false", k)
		}
	}
	if len(Kinds()) != 3 {
		t.Errorf("Kinds() = %v, want the three shipped kinds", Kinds())
	}
}
