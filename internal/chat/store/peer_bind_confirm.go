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

package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
)

// NewBindCode mints the code the console hands to the user for the
// /bind-onex handshake.
//
// 32 random bytes in base64url (43 characters) is the same shape the reference
// implementation uses. It is not a secret the console must remember: the row in
// chat.user_bind_code says which account minted it, and the bot's claim plus the
// confirmation code prove who spoke it. The length is what makes guessing a
// live code impractical while the handshake is open.
func NewBindCode() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("chat: could not read randomness for a bind code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashBindConfirm hashes what the user typed into the console, using the same
// digest the bot stored when it issued the confirmation code (see hashConfirm).
func HashBindConfirm(confirm string) string { return hashConfirm(confirm) }

// confirmCodeDigits is how long the confirmation code the bot speaks in private
// is. Eight decimal digits, because the person on the other end has to read it
// off one screen and type it into another.
//
// # It is not the security boundary, and this is the sentence to keep
//
// The code that keeps a leaked binding code from being claimed by the wrong
// person is the ownership check on the business side — the one that reads the
// binding code and finds it names somebody else, and stops there. A confirmation
// code only has to be read by the person who spoke, and never reaches anyone who
// did not: the bot hands it over inside the private conversation it heard the
// binding code in, so a member of a group who read the binding code there gets
// their own confirmation code for their own attempt, in their own conversation,
// and nothing else.
//
// So eight digits is chosen for the length of a person's attention, not for
// entropy. What the length buys is that a later change cannot quietly turn this
// into the only thing standing between an attacker and the binding — the audit
// of "would six digits have been enough?" is meant to end at the ownership
// check, not here.
const confirmCodeDigits = 8

// confirmCodeModulus is 10^confirmCodeDigits, the exclusive upper bound drawn
// from. Kept as a constant so the digit count and the draw cannot drift apart.
var confirmCodeModulus = func() *big.Int {
	n := big.NewInt(1)
	for i := 0; i < confirmCodeDigits; i++ {
		n.Mul(n, big.NewInt(10))
	}
	return n
}()

// newConfirmCode draws one confirmation code.
//
// rand.Int is the uniform draw over [0, modulus) — it rejects the samples that
// would bias a modulo, so the caller does not have to. A failure here is
// returned rather than papered over with a constant: a code the caller cannot
// know is one the binding can never complete, and a predictable one would be
// worse.
func newConfirmCode() (string, error) {
	n, err := rand.Int(rand.Reader, confirmCodeModulus)
	if err != nil {
		return "", fmt.Errorf("chat: could not read randomness for a binding confirmation code: %w", err)
	}
	// n is narrower than the modulus, so LeftPad the decimal form rather than
	// letting a code with leading zeros arrive one digit short.
	return fmt.Sprintf("%0*d", confirmCodeDigits, n), nil
}

// hashConfirm is what gets stored: the SHA-256 of the code's UTF-8 bytes, as
// lowercase hex.
//
// The business side computes the same digest over what the user typed and
// compares the two, so the plaintext code exists only in the message the bot
// sent and in the form the user filled in — never in this table, never in a log
// line this package writes. The value is a fixed 32 bytes before encoding, which
// is what lets the comparison be a constant-time one on both sides.
func hashConfirm(confirm string) string {
	sum := sha256.Sum256([]byte(confirm))
	return hex.EncodeToString(sum[:])
}
