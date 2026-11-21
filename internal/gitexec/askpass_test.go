package gitexec

import (
	"strings"
	"testing"
)

// However the server was started (a desktop session, an agent socket), git
// never gets a helper that can ask a person for a password.
func TestGitNeverPrompts(t *testing.T) {
	t.Setenv("SSH_ASKPASS", "/usr/libexec/openssh/gnome-ssh-askpass")
	t.Setenv("GIT_ASKPASS", "/usr/bin/zenity")
	t.Setenv("DISPLAY", ":0")
	env, cfg, err := Spec{Inherit: Remote}.build()
	if err != nil {
		t.Fatal(err)
	}
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nGIT_ASKPASS=/bin/false\n", "\nGIT_TERMINAL_PROMPT=0\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lacks %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(joined, "\nSSH_ASKPASS=") { // main's design: SSH_ASKPASS is never passed at all
		t.Errorf("SSH_ASKPASS was passed on:\n%s", joined)
	}
	if strings.Contains(joined, "DISPLAY=") || strings.Contains(joined, "zenity") || strings.Contains(joined, "gnome-ssh") {
		t.Errorf("a prompting helper or display leaked:\n%s", joined)
	}
	if !strings.Contains(strings.Join(cfg, " "), "core.askPass=") {
		t.Error("core.askPass is not overridden")
	}
}
