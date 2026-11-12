package errmsg

import (
	"errors"
	"strings"
	"testing"
)

func TestTextRedactsURLUserInfo(t *testing.T) {
	err := errors.New("dial https://user:secret@example.test/v1: connection refused")

	got := Text(err)

	if strings.Contains(got, "secret") || strings.Contains(got, "user:") {
		t.Fatalf("Text() leaked URL credentials: %q", got)
	}
	if !strings.Contains(got, "https://[redacted]@example.test/v1") {
		t.Fatalf("Text() should keep the endpoint shape, got %q", got)
	}
}

func TestTextRedactsSuppliedSecrets(t *testing.T) {
	err := errors.New("upstream rejected key sk-live-123")

	got := Text(err, "sk-live-123")

	if strings.Contains(got, "sk-live-123") {
		t.Fatalf("Text() leaked the secret: %q", got)
	}
}

func TestTextCollapsesAndTruncates(t *testing.T) {
	err := errors.New("line one\n\nline two   line three")

	got := Text(err)

	if strings.ContainsAny(got, "\n\t") || strings.Contains(got, "  ") {
		t.Fatalf("Text() should be one line, got %q", got)
	}

	long := errors.New(strings.Repeat("x", MaxRunes+10))
	got = Text(long)
	if len([]rune(got)) != MaxRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("Text() truncation = %d runes, want %d+ellipsis", len([]rune(got)), MaxRunes)
	}
}

func TestTextNilIsEmpty(t *testing.T) {
	if got := Text(nil); got != "" {
		t.Fatalf("Text(nil) = %q, want empty", got)
	}
}
