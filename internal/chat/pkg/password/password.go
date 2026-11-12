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

// Package password hashes and verifies the local account credential.
package password

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MinLength is the shortest password accepted. It is a floor, not a strength
	// policy: the point of using bcrypt is that the stored value survives a
	// database leak, and a longer password is the user's own choice.
	MinLength = 8
	// MaxLength bounds bcrypt's input. bcrypt silently truncates after 72 bytes,
	// so accepting more would give the user a password whose tail is not
	// actually checked. Refusing is the honest answer.
	MaxLength = 72
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{2,31}$`)

// ValidateUsername checks the login name's shape.
//
// The rule is deliberately narrow: the name appears in paths, command output and
// role-list configuration, and every one of those is easier to keep correct
// without spaces or characters that need escaping.
func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf(
			"用户名需为 3-32 位，以字母或数字开头，只允许字母、数字、下划线、点和连字符")
	}
	return nil
}

// ValidateNewPassword checks a password being set.
func ValidateNewPassword(secret string) error {
	if len(secret) < MinLength {
		return fmt.Errorf("密码至少需要 %d 位", MinLength)
	}
	if len(secret) > MaxLength {
		return fmt.Errorf("密码不能超过 %d 个字节", MaxLength)
	}
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("密码不能全是空白")
	}
	return nil
}

// Hash returns the bcrypt hash of secret.
func Hash(secret string) (string, error) {
	if err := ValidateNewPassword(secret); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// Verify answers whether secret matches hash.
//
// An empty hash is "this account has no password" rather than a mismatch worth
// distinguishing to the caller: both mean the password cannot be used to log in,
// and the caller turns that into the same message (see authBiz.Login).
func Verify(hash, secret string) bool {
	if hash == "" || secret == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) == nil
}

// NeedsHash reports whether the stored credential is a bcrypt hash this package
// can verify. It exists so a future hash-format migration can re-hash on login
// rather than silently failing every stored password.
func NeedsHash(hash string) bool {
	if hash == "" {
		return false
	}
	_, err := bcrypt.Cost([]byte(hash))
	return err == nil
}
