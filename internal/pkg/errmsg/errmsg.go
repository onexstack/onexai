// Package errmsg turns an internal error into a bounded, single-line string
// that is safe to put in a response body.
//
// Some endpoints answer 200 with a business result that carries a failure text
// (a connectivity probe, a batch delete report, a tool result). That text must
// not be err.Error() verbatim: it can carry a database statement, an upstream
// URL with credentials, or an unbounded provider body. This is the one place
// those conversions go through.
package errmsg

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxRunes bounds one message. It is long enough for an actionable upstream
// explanation and short enough that a UI can render it in one notification.
const MaxRunes = 300

// userInfo matches the credential part of a URL: scheme://user:pass@host.
var userInfo = regexp.MustCompile(`://[^/@\s]+:[^/@\s]+@`)

// Text returns a bounded, single-line description of err with URL credentials
// and any supplied secrets redacted. A nil error yields an empty string.
func Text(err error, secrets ...string) string {
	if err == nil {
		return ""
	}

	s := collapse(err.Error())
	s = userInfo.ReplaceAllString(s, "://[redacted]@")
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}

	return truncate(s, MaxRunes)
}

// String applies the same treatment to an already-formatted message.
func String(s string, secrets ...string) string {
	return Text(errorString(s), secrets...)
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

type errorString string

func (e errorString) Error() string { return string(e) }
