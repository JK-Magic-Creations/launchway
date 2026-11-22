package pipeline

import (
	"strings"
	"testing"
)

// A person's text inside a message must not be able to end the quotes early
// or carry a raw control character.
func TestQuoteTextEscapes(t *testing.T) {
	got := quoteText("a\"b\\c\nd")
	if got != `"a\"b\\c\nd"` {
		t.Fatalf("quoteText = %s", got)
	}
	_, msg := CheckInto("/abs\"\x01")
	if strings.ContainsAny(msg, "\x01") || !strings.Contains(msg, `\"`) {
		t.Fatalf("message not escaped: %q", msg)
	}
}
