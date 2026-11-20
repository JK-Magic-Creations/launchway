package gitexec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// homeDir is an empty directory that stands in for HOME, so git finds no
// ~/.gitconfig, no credentials store and no templates there.
var (
	homeOnce sync.Once
	homePath string
)

func emptyHome() string {
	homeOnce.Do(func() {
		d, err := os.MkdirTemp("", "launchway-git-home-")
		if err != nil {
			homePath = "/nonexistent"
			return
		}
		// Read-only: git cannot write anything into it.
		_ = os.Chmod(d, 0o500) //nolint:gosec // read-only on purpose: git can write nothing into the stand-in HOME
		homePath = d
	})
	return homePath
}

// localVars are passed to every command.
var localVars = []string{"PATH", "TMPDIR", "TZ"}

// remoteVars are also passed to commands that talk to a remote.
var remoteVars = []string{
	"SSH_AUTH_SOCK", "GIT_SSH", "GIT_SSH_VARIANT",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO", "GIT_SSL_CAPATH",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
}

// allowedEnv are the names a Spec may add. A name that is not here is
// refused: one variable inherited or passed by accident can defeat the
// isolation (GIT_EXTERNAL_DIFF, GIT_PROXY_COMMAND, GIT_EXEC_PATH,
// GIT_TEMPLATE_DIR, GIT_TRACE and the like all can).
var allowedEnv = map[string]bool{
	"GIT_CONFIG_COUNT": true, "GIT_INDEX_FILE": true,
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true, "GIT_AUTHOR_DATE": true,
	"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true, "GIT_COMMITTER_DATE": true,
	"GIT_SSH_COMMAND": true, "GIT_SSH": true, "GIT_SSH_VARIANT": true, "SSH_AUTH_SOCK": true,
	"GIT_LITERAL_PATHSPECS": true, "GIT_OBJECT_DIRECTORY": true, "GIT_QUARANTINE_PATH": true,
	"GIT_WORK_TREE": true, "GIT_NAMESPACE": true, "GIT_PROTOCOL": true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_REFLOG_ACTION": true,
	"GIT_HTTP_LOW_SPEED_LIMIT": true, "GIT_HTTP_LOW_SPEED_TIME": true,
	"GIT_SSL_CAINFO": true, "GIT_SSL_CAPATH": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
}

// AllowedEnv reports whether a Spec may set the variable.
func AllowedEnv(name string) bool {
	if allowedEnv[name] {
		return true
	}
	if strings.HasPrefix(name, "LAUNCHWAY_") {
		return true
	}
	for _, p := range []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"} {
		if strings.HasPrefix(name, p) && isDigits(name[len(p):]) {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isGitConfigVar(name string) bool {
	return name == "GIT_CONFIG_COUNT" || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

// build returns git's environment and the -c settings that precede its
// arguments.
func (s Spec) build() (env []string, cfg []string, err error) {
	given := map[string]string{}
	for _, kv := range s.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !AllowedEnv(k) {
			return nil, nil, fmt.Errorf("%w: environment variable %q", ErrDenied, strings.SplitN(kv, "=", 2)[0])
		}
		if k == "GIT_ALTERNATE_OBJECT_DIRECTORIES" && !s.AllowAlternates {
			return nil, nil, fmt.Errorf("%w: GIT_ALTERNATE_OBJECT_DIRECTORIES", ErrDenied)
		}
		given[k] = v
	}
	_, hasConfig := given["GIT_CONFIG_COUNT"]

	env = make([]string, 0, 32)
	add := func(k, v string) { env = append(env, k+"="+v) }
	pass := func(names []string) {
		for _, k := range names {
			if _, set := given[k]; set {
				continue
			}
			if v, ok := os.LookupEnv(k); ok {
				add(k, v)
			}
		}
	}
	pass(localVars)
	inheritedSSH := ""
	if s.Inherit == Remote {
		pass(remoteVars)
		if !hasConfig {
			for _, kv := range os.Environ() {
				k, _, _ := strings.Cut(kv, "=")
				if isGitConfigVar(k) && (k == "GIT_CONFIG_COUNT" || AllowedEnv(k)) {
					env = append(env, kv)
				}
			}
		}
		inheritedSSH = os.Getenv("GIT_SSH")
	}
	// What git always sees. These are set last by name below, so nothing
	// given or inherited can override them.
	fixed := map[string]string{
		"HOME":                   emptyHome(),
		"XDG_CONFIG_HOME":        filepath.Join(emptyHome(), ".config"),
		"GIT_CONFIG_NOSYSTEM":    "1",
		"GIT_CONFIG_GLOBAL":      os.DevNull,
		"GIT_TERMINAL_PROMPT":    "0",
		"GIT_ASKPASS":            "",
		"SSH_ASKPASS":            "",
		"GIT_PAGER":              "cat",
		"LC_ALL":                 "C",
		"GIT_OPTIONAL_LOCKS":     "0",
		"GIT_NO_REPLACE_OBJECTS": "1",
	}
	if s.GitDir != "" {
		fixed["GIT_DIR"] = s.GitDir
	}
	if s.Dir != "" {
		fixed["GIT_CEILING_DIRECTORIES"] = filepath.Dir(filepath.Clean(s.Dir))
	}
	for k, v := range given {
		if _, f := fixed[k]; f {
			continue
		}
		add(k, v)
	}
	for k, v := range fixed {
		add(k, v)
	}
	// The environment may name each variable once; a later duplicate would
	// win in some programs and lose in others.
	env = dedupe(env)

	hooks := s.HooksPath
	if hooks == "" {
		hooks = os.DevNull
	}
	ssh := s.SSHCommand
	if ssh == "" {
		ssh = inheritedSSH
	}
	if ssh == "" {
		ssh = "ssh"
	}
	cfg = []string{
		"-c", "core.hooksPath=" + hooks,
		"-c", "core.fsmonitor=false",
		"-c", "core.askPass=",
		"-c", "credential.helper=",
		"-c", "core.protectNTFS=true",
		"-c", "core.protectHFS=true",
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=user",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "advice.detachedHead=false",
	}
	if _, explicit := given["GIT_SSH_COMMAND"]; !explicit {
		// The repository's own core.sshCommand must not run; naming one on
		// the command line beats it.
		cfg = append(cfg, "-c", "core.sshCommand="+ssh)
	}
	if s.Untrusted {
		cfg = append(cfg, "-c", "transfer.fsckObjects=true", "-c", "submodule.recurse=false")
	}
	return env, cfg, nil
}

func dedupe(env []string) []string {
	seen := map[string]int{}
	out := env[:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if i, ok := seen[k]; ok {
			out[i] = kv
			continue
		}
		seen[k] = len(out)
		out = append(out, kv)
	}
	return out
}
