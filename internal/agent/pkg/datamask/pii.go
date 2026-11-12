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
	"fmt"
	"regexp"
	"strings"
)

// piiRule masks one identifier shape. value is the capture group holding the
// identifier itself; the groups around it exist because Go's regular expressions
// have no lookaround, so "not preceded by a digit" has to be spelled as a
// captured boundary that the replacement puts back.
type piiRule struct {
	// id names the rule, and appears in the marker a wholesale replacement
	// leaves behind.
	id    string
	re    *regexp.Regexp
	value int
	mask  func(string) string
}

// piiRules are applied in order, so a shape that contains another shape has to
// come first: a 16-digit card number contains an 11-digit run that the phone
// pattern would otherwise claim.
var piiRules = []piiRule{
	// Coordinates are replaced whole: a location is not a value with a
	// recognizable part, it is the location.
	{
		id:   "coordinates",
		re:   regexp.MustCompile(`-?\d{1,3}(?:\.\d+)?\s*[,，]\s*-?\d{1,3}(?:\.\d+)?`),
		mask: func(string) string { return "[REDACTED:coordinates]" },
	},
	// Mainland China mobile: keep 3 leading and 2 trailing digits.
	{
		id:    "phone",
		re:    regexp.MustCompile(`(^|\D)(1[3-9]\d{9})(\D|$)`),
		value: 2,
		mask:  func(s string) string { return keepEdges(s, MaskMid, 3, 2) },
	},
	{
		id: "email",
		// "*" is in the local-part class on purpose: it makes the rule match
		// its own output ("z****n@example.com") as a whole. Without it, a
		// second pass would match only the "n" before the "@" and mask that,
		// producing a different — and longer-masked — result. Masking must be
		// idempotent: an audit record is masked on write and again on read.
		re:    regexp.MustCompile(`[\w.*+-]+@[\w-]+\.[\w.]+`),
		value: 0,
		mask:  maskEmail,
	},
	// Hong Kong identity card: one letter, six digits, optional check digit.
	{
		id:    "hk-id",
		re:    regexp.MustCompile(`(^|[^A-Za-z0-9])([A-Z]\d{6}\(?[0-9A]\)?)([^A-Za-z0-9]|$)`),
		value: 2,
		mask:  func(s string) string { return keepEdges(s, MaskMid, 2, 2) },
	},
	// Residence ID: 4 leading and 2 trailing characters survive.
	{
		id:    "residence-id",
		re:    regexp.MustCompile(`(^|\D)([1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:[0-2]\d|3[01])\d{3}[\dXx])(\D|$)`),
		value: 2,
		mask:  func(s string) string { return keepEdges(s, MaskMid, 4, 2) },
	},
	// Bank cards and account numbers: only the last four digits are useful for
	// telling two of them apart, and they identify nothing on their own.
	{
		id:    "bank-card",
		re:    regexp.MustCompile(`(^|\D)(\d{16,19})(\D|$)`),
		value: 2,
		mask:  func(s string) string { return keepSuffix(s, MaskHead, 4) },
	},
	// Passport: one or two letters then six to nine digits.
	{
		id:    "passport",
		re:    regexp.MustCompile(`(^|[^A-Za-z0-9])([A-Z]{1,2}\d{6,9})([^A-Za-z0-9]|$)`),
		value: 2,
		mask:  func(s string) string { return keepEdges(s, MaskMid, 2, 2) },
	},
	// Device identifiers (IMEI and friends): last four digits.
	{
		id:    "device-id",
		re:    regexp.MustCompile(`(^|\D)(\d{15})(\D|$)`),
		value: 2,
		mask:  func(s string) string { return keepSuffix(s, MaskHead, 4) },
	},
	{
		id:    "ipv4",
		re:    regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`),
		value: 0,
		mask:  maskIPv4,
	},
	{
		id:    "ipv6",
		re:    regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}\b`),
		value: 0,
		mask:  maskIPv6,
	},
	{
		id:    "mac",
		re:    regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}\b`),
		value: 0,
		mask:  maskMAC,
	},
	// Landline: the area code is public, the subscriber number is not.
	{
		id:    "landline",
		re:    regexp.MustCompile(`(0\d{2,3}-)(\d{7,8})`),
		value: 2,
		mask:  func(s string) string { return keepEdges(s, MaskMid, 2, 2) },
	},
	// An address down to the district is useful for context; the street level
	// is where a person lives. Two administrative levels are required so that a
	// word like "supermarket" is not mistaken for one.
	{
		id:    "address",
		re:    regexp.MustCompile(`[\p{Han}]{2,}(?:省|市)[\p{Han}]{2,}(?:市|区|县|州)[\p{Han}0-9A-Za-z号栋楼室-]{1,}`),
		value: 0,
		mask:  maskAddressRegion,
	},
}

// MaskPII partially masks the identifiers in text, leaving the rest untouched.
// It is the storage-boundary form: a value stays recognizable to whoever is
// investigating without exposing the whole of it.
func MaskPII(text string) string {
	return applyPII(text, false)
}

// MaskPIIFully replaces every identifier wholesale with a marker naming the
// rule. It is the live-output form: text on its way out of the service is not
// something a reader needs to recognize, and a partially masked value is still
// a value.
func MaskPIIFully(text string) string {
	return applyPII(text, true)
}

// applyPII runs the rules, either partially masking each match or replacing it
// whole.
func applyPII(text string, wholesale bool) string {
	if text == "" {
		return text
	}
	for _, rule := range piiRules {
		if wholesale {
			text = rule.redact(text)
			continue
		}
		text = rule.apply(text)
	}
	return text
}

// redact replaces the value group of every match with a marker.
func (r piiRule) redact(text string) string {
	return r.replace(text, func(string) string { return fmt.Sprintf(redactedFormat, r.id) })
}

// apply replaces the value group of every match, leaving the boundary groups
// exactly as they were.
func (r piiRule) apply(text string) string {
	return r.replace(text, r.mask)
}

// replace rewrites the value group of every match, leaving the boundary groups
// exactly as they were.
func (r piiRule) replace(text string, mask func(string) string) string {
	return r.re.ReplaceAllStringFunc(text, func(match string) string {
		idx := r.re.FindStringSubmatchIndex(match)
		if r.value*2+1 >= len(idx) || idx[r.value*2] < 0 {
			return match
		}
		start, end := idx[r.value*2], idx[r.value*2+1]
		return match[:start] + mask(match[start:end]) + match[end:]
	})
}

// keepEdges keeps keepPrefix leading and keepSuffix trailing characters and
// replaces everything between them.
func keepEdges(s, mask string, keepPrefix, keepSuffix int) string {
	runes := []rune(s)
	if len(runes) <= keepPrefix+keepSuffix {
		return mask
	}
	return string(runes[:keepPrefix]) + mask + string(runes[len(runes)-keepSuffix:])
}

// keepSuffix keeps the trailing characters and replaces everything before them.
func keepSuffix(s, mask string, keepSuffix int) string {
	runes := []rune(s)
	if len(runes) <= keepSuffix {
		return mask
	}
	return mask + string(runes[len(runes)-keepSuffix:])
}

// maskEmail keeps the first and last character of the local part and the whole
// domain: the domain is what says whether the address is even interesting.
func maskEmail(s string) string {
	at := strings.LastIndexByte(s, '@')
	if at <= 0 {
		return MaskMid
	}
	return keepEdges(s[:at], MaskMid, 1, 1) + s[at:]
}

// maskIPv4 keeps the last two octets, which identify the host within its
// network without naming the network.
func maskIPv4(s string) string {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return MaskHead
	}
	return MaskHead + "." + strings.Join(parts[2:], ".")
}

// maskIPv6 keeps the last six groups, for the same reason.
func maskIPv6(s string) string {
	parts := strings.Split(s, ":")
	if len(parts) <= 6 {
		return MaskHead
	}
	return MaskHead + ":" + strings.Join(parts[len(parts)-6:], ":")
}

// maskMAC keeps the first three octets: they are the vendor, not the device.
func maskMAC(s string) string {
	parts := strings.Split(s, ":")
	if len(parts) <= 3 {
		return MaskHead
	}
	return strings.Join(parts[:3], ":") + ":" + MaskHead
}

// maskAddressRegion keeps the administrative levels and drops the street.
func maskAddressRegion(s string) string {
	// Cut after the second administrative suffix (province/city, then
	// district/county), which is where the identifying part begins.
	runes := []rune(s)
	count := 0
	for i, r := range runes {
		switch r {
		case '省', '市', '区', '县', '州':
			count++
			if count == 2 {
				return string(runes[:i+1]) + MaskHead
			}
		}
	}
	return s
}
