package localauth

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// The password policy follows NIST SP 800-63B-4: a minimum length, a high
// maximum, no composition rules (a mix of cases and symbols is not asked
// for), a check against passwords known to be common or breached, and no
// forced rotation (a password changes when someone chooses to, or when it is
// known to be exposed).
const (
	MinPasswordLength = 12  // characters; the second factor is required of administrators by default
	MaxPasswordLength = 256 // characters; Argon2id takes any length, this only bounds the request
)

//go:embed commonpasswords.txt
var commonPasswordsText string

var (
	commonOnce sync.Once
	common     map[string]struct{}
)

func commonSet() map[string]struct{} {
	commonOnce.Do(func() {
		common = map[string]struct{}{}
		for _, l := range strings.Split(commonPasswordsText, "\n") {
			if l = strings.TrimSpace(strings.ToLower(l)); l != "" {
				common[l] = struct{}{}
			}
		}
	})
	return common
}

// CommonPasswordCount is how many entries the shipped blocklist holds.
func CommonPasswordCount() int { return len(commonSet()) }

// CheckPassword returns a plain sentence saying what is wrong with the
// password, or "" if it is acceptable. context names things the password
// must not be built from (the user name, the display name, the site name).
func CheckPassword(password string, context ...string) string {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinPasswordLength:
		return fmt.Sprintf("Use at least %d characters. A few ordinary words in a row make a long password that is easy to remember.", MinPasswordLength)
	case n > MaxPasswordLength:
		return fmt.Sprintf("Use at most %d characters.", MaxPasswordLength)
	}
	low := strings.ToLower(password)
	if _, bad := commonSet()[low]; bad {
		return "That password is on the list of common or leaked passwords. Choose something that is not."
	}
	// A common password with a small decoration is just as guessable.
	stripped := strings.Trim(low, "0123456789!@#$%^&*()_+-=.,? ")
	if _, bad := commonSet()[stripped]; bad && stripped != "" {
		return "That password is a common one with a few characters added. Choose something that is not."
	}
	if allSame(password) || isRun(low) {
		return "That password is a repeated or sequential pattern. Choose something harder to guess."
	}
	for _, c := range context {
		c = strings.ToLower(strings.TrimSpace(c))
		if len(c) >= 4 && strings.Contains(low, c) {
			return "The password contains your name or the name of this site. Choose something else."
		}
	}
	return ""
}

func allSame(s string) bool {
	r := []rune(s)
	for _, c := range r {
		if c != r[0] {
			return false
		}
	}
	return true
}

// isRun reports a string that is one ascending or descending run of
// characters such as "abcdefghijkl" or "987654321098".
func isRun(s string) bool {
	r := []rune(s)
	if len(r) < 8 {
		return false
	}
	d := r[1] - r[0]
	if d != 1 && d != -1 {
		return false
	}
	for i := 1; i < len(r); i++ {
		if r[i]-r[i-1] != d {
			return false
		}
	}
	return true
}
