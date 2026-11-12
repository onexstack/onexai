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

package datamask

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantAbsent string
		wantMarker string
	}{
		{
			name:       "aws access key",
			in:         "the key is AKIAIOSFODNN7EXAMPLE ok",
			wantAbsent: "AKIAIOSFODNN7EXAMPLE",
			wantMarker: "[REDACTED:aws-access-key]",
		},
		{
			name:       "github token",
			in:         "token ghp_abcdefghijklmnopqrstuvwxyz0123456789 here",
			wantAbsent: "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
			wantMarker: "[REDACTED:github-token]",
		},
		{
			name:       "bearer header",
			in:         "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			wantAbsent: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			wantMarker: "[REDACTED:bearer]",
		},
		{
			name:       "private key block",
			in:         "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
			wantAbsent: "MIIEow",
			wantMarker: "[REDACTED:private-key]",
		},
		{
			name:       "assigned password keeps the field name",
			in:         "config: password = hunter2secret",
			wantAbsent: "hunter2secret",
			wantMarker: "password = [REDACTED:assigned-secret]",
		},
		{
			name:       "assigned api key in json",
			in:         `{"api_key":"abcdef1234567890"}`,
			wantAbsent: "abcdef1234567890",
			wantMarker: "[REDACTED:assigned-secret]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.in)
			if strings.Contains(got, tc.wantAbsent) {
				t.Errorf("the secret survived: %q", got)
			}
			if !strings.Contains(got, tc.wantMarker) {
				t.Errorf("expected marker %q in %q", tc.wantMarker, got)
			}
		})
	}
}

// TestRedactSecrets_VendorAndShapeRules covers the rules added for the shapes a
// credential actually arrives in: a DSN, a URL with a password, a vendor key.
func TestRedactSecrets_VendorAndShapeRules(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantAbsent []string
		wantMarker string
	}{
		{
			name:       "stripe key",
			in:         "key sk_live_abcdefghijklmnopqrstuvwx here",
			wantAbsent: []string{"sk_live_abcdefghijklmnopqrstuvwx"},
			wantMarker: "[REDACTED:stripe-key]",
		},
		{
			name:       "google api key",
			in:         "AIzaSyA1234567890abcdefghijklmnopqrstuv",
			wantAbsent: []string{"AIzaSyA1234567890abcdefghijklmnopqrstuv"},
			wantMarker: "[REDACTED:google-api-key]",
		},
		{
			name:       "password in a url",
			in:         "connect to https://admin:hunter2@db.internal/app",
			wantAbsent: []string{"hunter2"},
			wantMarker: "[REDACTED:credentials-in-url]",
		},
		{
			name:       "database dsn",
			in:         "dsn=postgres://agent:s3cretpw@data01-prod:5432/zagent",
			wantAbsent: []string{"s3cretpw"},
			wantMarker: "[REDACTED:db-connection-string]",
		},
		{
			name:       "basic auth header",
			in:         "Basic YWRtaW46aHVudGVyMmh1bnRlcjI=",
			wantAbsent: []string{"YWRtaW46aHVudGVyMmh1bnRlcjI="},
			wantMarker: "[REDACTED:basic-auth]",
		},
		{
			name:       "authorization header without a scheme",
			in:         `authorization: "abcdefghijklmnopqrstuvwxyz"`,
			wantAbsent: []string{"abcdefghijklmnopqrstuvwxyz"},
			wantMarker: "[REDACTED:authorization-header]",
		},
		{
			name:       "one-time code",
			in:         "otp_code = 481920",
			wantAbsent: []string{"481920"},
			wantMarker: "[REDACTED:one-time-code]",
		},
		{
			name:       "authenticator seed",
			in:         "google_auth_seed: JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP",
			wantAbsent: []string{"JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"},
			wantMarker: "[REDACTED:authenticator-seed]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.in)
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("the secret survived: %q", got)
				}
			}
			if !strings.Contains(got, tc.wantMarker) {
				t.Errorf("expected marker %q in %q", tc.wantMarker, got)
			}
		})
	}
}

// TestRedactSecrets_DoesNotEatOrdinaryURLs guards the rule that is easiest to
// get wrong: a URL with a port has a colon, and a URL with a user name has an
// "@", but neither is a credential.
func TestRedactSecrets_DoesNotEatOrdinaryURLs(t *testing.T) {
	for _, text := range []string{
		"see https://example.com:8443/health for status",
		"mail go.kong@example.com about it",
		"https://api.internal/v1/models",
		"the service listens on 10.0.0.1:8080",
	} {
		if got := RedactSecrets(text); got != text {
			t.Errorf("RedactSecrets(%q) = %q, want it unchanged", text, got)
		}
	}
}

// TestRedactSecrets_IsIdempotent pins that masking already-masked text changes
// nothing. It is the property most easily broken by adding a rule: a broad rule
// that matches its own marker rewrites it into a different one, and a record
// masked on write and again on read would drift.
func TestRedactSecrets_IsIdempotent(t *testing.T) {
	inputs := []string{
		"password = hunter2secret",
		"postgres://agent:s3cretpw@data01-prod:5432/zagent",
		"https://admin:hunter2@db.internal/app",
		"AKIAIOSFODNN7EXAMPLE and sk_live_abcdefghijklmnopqrstuvwx",
		"otp_code = 481920",
		`authorization: "abcdefghijklmnopqrstuvwxyz"`,
		"Basic YWRtaW46aHVudGVyMmh1bnRlcjI=",
		"session_id=abcdefghijklmnop123456",
	}
	for _, in := range inputs {
		once := RedactSecrets(in)
		if twice := RedactSecrets(once); twice != once {
			t.Errorf("not idempotent for %q: once=%q twice=%q", in, once, twice)
		}
	}
}

func TestRedactSecrets_LeavesOrdinaryTextAlone(t *testing.T) {
	const text = "Deploy the service to the staging namespace and check the logs."
	if got := RedactSecrets(text); got != text {
		t.Errorf("ordinary prose was rewritten: %q", got)
	}
}

func TestMaskPII(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantAbsent string
		wantIn     string
	}{
		{
			name:       "mobile number",
			in:         "call 13812345678 now",
			wantAbsent: "13812345678",
			wantIn:     "138****78",
		},
		{
			name:       "email",
			in:         "write to zhangsan@example.com please",
			wantAbsent: "zhangsan@example.com",
			wantIn:     "@example.com",
		},
		{
			name:       "bank card",
			in:         "card 6222021234567890 charged",
			wantAbsent: "6222021234567890",
			wantIn:     "7890",
		},
		{
			name:       "ipv4 keeps the host part",
			in:         "host 10.20.30.40 responded",
			wantAbsent: "10.20.30.40",
			wantIn:     "30.40",
		},
		{
			name:       "mac keeps the vendor",
			in:         "mac A4:5E:60:11:22:33 seen",
			wantAbsent: "A4:5E:60:11:22:33",
			wantIn:     "A4:5E:60",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MaskPII(tc.in)
			if strings.Contains(got, tc.wantAbsent) {
				t.Errorf("the identifier survived: %q", got)
			}
			if !strings.Contains(got, tc.wantIn) {
				t.Errorf("expected %q to be kept in %q", tc.wantIn, got)
			}
		})
	}
}

// TestMaskPII_DoesNotEatShortNumbers guards against the boundary handling
// turning an ordinary number into a masked one.
func TestMaskPII_DoesNotEatShortNumbers(t *testing.T) {
	for _, text := range []string{
		"replica count is 3",
		"port 8080 is in use",
		"commit abc1234",
		"latency 250ms",
		"the year 2026",
	} {
		if got := MaskPII(text); got != text {
			t.Errorf("MaskPII(%q) = %q, want it unchanged", text, got)
		}
	}
}

func TestMaskPII_CoordinatesAreRemovedWhole(t *testing.T) {
	got := MaskPII("location 31.2304,121.4737 reported")
	if strings.Contains(got, "31.2304") || strings.Contains(got, "121.4737") {
		t.Errorf("coordinates survived: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:coordinates]") {
		t.Errorf("expected a coordinates marker in %q", got)
	}
}

// TestMask_IsIdempotent pins that masking already-masked text changes nothing:
// an audit record can be masked on write and again on read.
func TestMask_IsIdempotent(t *testing.T) {
	const text = "user zhangsan@example.com reported password = hunter2secret from 10.20.30.40"
	once := Mask(text)
	twice := Mask(once)
	if once != twice {
		t.Errorf("masking is not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
}

func TestMask_EmptyIsEmpty(t *testing.T) {
	if got := Mask(""); got != "" {
		t.Errorf("Mask(\"\") = %q", got)
	}
}

func TestExtraSecretPatterns(t *testing.T) {
	ExtraSecretPatterns(map[string]string{"internal-token": `INT-[A-Z0-9]{12}`})
	t.Cleanup(func() {
		extraRulesMu.Lock()
		extraRules = nil
		extraRulesMu.Unlock()
	})

	got := RedactSecrets("use INT-ABCDEFGH1234 for access")
	if strings.Contains(got, "INT-ABCDEFGH1234") {
		t.Errorf("the registered pattern did not fire: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:internal-token]") {
		t.Errorf("expected the rule id in the marker: %q", got)
	}
}
