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

package larkoauth

import (
	"strings"
	"testing"
)

func TestStateRoundTripsTheRedirect(t *testing.T) {
	signer := NewStateSigner("test-secret")
	state, err := signer.Sign("/workbench")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := signer.Verify(state)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != "/workbench" {
		t.Errorf("redirect = %q, want /workbench", got)
	}
}

func TestStateRejectsTampering(t *testing.T) {
	signer := NewStateSigner("test-secret")
	state, err := signer.Sign("/workbench")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Flip one character in the payload half. The signature no longer matches.
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("state has no signature: %q", state)
	}
	tampered := parts[0] + "x." + parts[1]
	if _, err := signer.Verify(tampered); err == nil {
		t.Fatal("a tampered state verified")
	}

	if _, err := NewStateSigner("other-secret").Verify(state); err == nil {
		t.Fatal("a state signed with another key verified")
	}
}

func TestStateRejectsCrossSiteRedirects(t *testing.T) {
	signer := NewStateSigner("test-secret")
	for _, redirect := range []string{"https://evil.example", "//evil.example", "javascript:alert(1)"} {
		if _, err := signer.Sign(redirect); err == nil {
			t.Errorf("Sign(%q) accepted an off-site redirect", redirect)
		}
	}
}
