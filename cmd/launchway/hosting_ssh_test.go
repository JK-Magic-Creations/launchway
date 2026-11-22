package main

import (
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
)

// What `launchway keys host` prints is what the server tells it: the public host and the public port, not the
// address the server listens on.
func TestSSHInfoNamesThePublicPortAndHost(t *testing.T) {
	for name, tc := range map[string]struct {
		forge    config.ForgeConfig
		host     string
		port     int
		hasCerts bool
	}{
		"public port and host win over listen": {
			config.ForgeConfig{PublicURL: "https://git.acme.test", SSH: config.ForgeSSHConfig{Listen: ":2222", PublicHost: "ssh.acme.test", PublicPort: 22}},
			"ssh.acme.test", 22, false},
		"listen port when no public port": {
			config.ForgeConfig{PublicURL: "https://git.acme.test", SSH: config.ForgeSSHConfig{Listen: "0.0.0.0:2222"}},
			"git.acme.test", 2222, false},
		"public port with the host of the public url": {
			config.ForgeConfig{PublicURL: "https://git.acme.test:8443", SSH: config.ForgeSSHConfig{Listen: ":2222", PublicPort: 2200}},
			"git.acme.test", 2200, true},
	} {
		got := sshInfo(tc.forge, tc.hasCerts)
		if !got.Enabled || got.Host != tc.host || got.Port != tc.port || got.Certificates != tc.hasCerts {
			t.Errorf("%s: %+v, want host %s port %d", name, got, tc.host, tc.port)
		}
	}
	// With neither a public host nor a public URL the machine's own name is used, so the host is never empty.
	if got := sshInfo(config.ForgeConfig{SSH: config.ForgeSSHConfig{Listen: ":2222"}}, false); got.Host == "" {
		t.Errorf("no host: %+v", got)
	}
}
