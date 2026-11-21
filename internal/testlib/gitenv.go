package testlib

import (
	"os"
	"path/filepath"
	"testing"
)

// NoPrompt makes every Git client a test starts unable to ask a person for
// anything. Git falls back to SSH_ASKPASS for HTTP credentials even with
// GIT_TERMINAL_PROMPT=0, and on a desktop session that is a graphical dialog,
// so SSH_ASKPASS goes, GIT_ASKPASS refuses, and the user's and the system's
// Git configuration (credential helpers among it) are not read. Call it from
// TestMain of every package whose tests run a Git client.
func NoPrompt() {
	os.Unsetenv("SSH_ASKPASS")
	os.Setenv("GIT_ASKPASS", "/bin/false")
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
}

// GitEnv is a complete environment for a Git client run by a test through
// exec.Cmd.Env: the process's own, but with nothing that can prompt, no
// credential helper, no system or global configuration, and a temporary HOME.
func GitEnv(t testing.TB, extra ...string) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		k := kv
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				k = kv[:i]
				break
			}
		}
		switch k {
		case "SSH_ASKPASS", "GIT_ASKPASS", "GIT_TERMINAL_PROMPT", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "HOME",
			"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+filepath.Join(t.TempDir(), "home"), "GIT_ASKPASS=/bin/false", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.askPass", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=")
	return append(env, extra...)
}
