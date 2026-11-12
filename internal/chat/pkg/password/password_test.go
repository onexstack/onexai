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

package password

import "testing"

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if hash == "correct horse battery staple" {
		t.Fatal("the stored value is the password itself")
	}
	if !Verify(hash, "correct horse battery staple") {
		t.Error("the correct password did not verify")
	}
	if Verify(hash, "wrong") {
		t.Error("a wrong password verified")
	}
	if Verify("", "anything") {
		t.Error("an account with no password verified")
	}
	if !NeedsHash(hash) {
		t.Error("a bcrypt hash was not recognised")
	}
	if NeedsHash("") {
		t.Error("an empty credential was recognised as a hash")
	}
}

func TestHashRejectsShortAndOversizedPasswords(t *testing.T) {
	if _, err := Hash("short"); err == nil {
		t.Error("a short password was accepted")
	}
	long := make([]byte, MaxLength+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Hash(string(long)); err == nil {
		t.Error("a password longer than bcrypt's input limit was accepted")
	}
}

func TestUsernameShape(t *testing.T) {
	valid := []string{"abc", "go.kong", "user_1", "a-b-c"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("ValidateUsername(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "ab", "has space", "-leading", "a/../b"}
	for _, name := range invalid {
		if err := ValidateUsername(name); err == nil {
			t.Errorf("ValidateUsername(%q) accepted an invalid name", name)
		}
	}
}
