package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EncryptionConfig is where the installation's root key is held and how
// encryption at rest behaves. It is a bootstrap setting: it is read from the
// file (and environment) before the database can be opened, and is never kept
// in the database, whose secrets it protects.
//
// With nothing set, a new installation generates a key file in its data
// directory; an existing installation keeps starting as it did, "not
// encrypted at rest", with a warning, until a holder is configured.
type EncryptionConfig struct {
	// Holder picks the kind: key_file, env, passphrase or transit. Empty: the
	// one thing that is set (more than one is refused).
	Holder string `yaml:"holder"`
	// KeyFile is a file of 32 random bytes in base64, mode 0600 (or a mounted
	// Secret's). The environment variable LAUNCHWAY_ROOT_KEY_FILE overrides it.
	KeyFile string `yaml:"key_file"`
	// The environment variable LAUNCHWAY_ROOT_KEY holds the key itself (the env holder).
	// PreviousKeyFile is the root key being replaced, given beside the new
	// one while `launchway encryption rewrap` runs
	// (LAUNCHWAY_ROOT_KEY_PREVIOUS_FILE).
	PreviousKeyFile string `yaml:"previous_key_file"`
	// Keystore is a passphrase keystore file (made by `launchway encryption
	// init --passphrase`); PassphraseFile holds the passphrase (first line),
	// or it is asked for at start on a terminal.
	Keystore       string `yaml:"keystore_file"`
	PassphraseFile string `yaml:"passphrase_file"`
	// Transit keeps the root key in Vault or OpenBao's transit engine.
	Transit TransitConfig `yaml:"transit"`
	// Require refuses to start with no root key, even on an installation that
	// has none yet and would generate one.
	Require bool `yaml:"require"`
	// NoGenerate stops a new installation from generating a key file by itself
	// (it then runs not encrypted, with the warning).
	NoGenerate bool `yaml:"no_generate"`
}

// TransitConfig is a Vault or OpenBao transit key: the connection settings of
// a vault secret store, plus the transit mount and key.
type TransitConfig struct {
	Address   string `yaml:"address"`
	Mount     string `yaml:"mount"` // the transit engine's mount; default transit
	Key       string `yaml:"key"`
	Namespace string `yaml:"namespace"`
	CAFile    string `yaml:"ca_file"`
	TokenEnv  string `yaml:"token_env"`
	TokenFile string `yaml:"token_file"`
	Role      string `yaml:"role"`
	AuthPath  string `yaml:"auth_path"`
	JWTFile   string `yaml:"jwt_file"`
}

// Environment variables that set the root key.
const (
	EnvRootKey             = "LAUNCHWAY_ROOT_KEY"
	EnvRootKeyFile         = "LAUNCHWAY_ROOT_KEY_FILE"
	EnvRootKeyPreviousFile = "LAUNCHWAY_ROOT_KEY_PREVIOUS_FILE"
	EnvRootKeyPrevious     = "LAUNCHWAY_ROOT_KEY_PREVIOUS"
	EnvPassphraseFile      = "LAUNCHWAY_KEYSTORE_PASSPHRASE_FILE"
)

func (e *EncryptionConfig) relativeTo(base string) {
	for _, p := range []*string{&e.KeyFile, &e.PreviousKeyFile, &e.Keystore, &e.PassphraseFile, &e.Transit.CAFile, &e.Transit.TokenFile, &e.Transit.JWTFile} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
}

// Resolved is the holder kind in effect and where its key comes from, after
// the environment overrides. Kind is "" when nothing is configured.
type Resolved struct {
	Kind            string
	KeyFile         string
	PreviousKeyFile string
	Keystore        string
	PassphraseFile  string
}

// Resolve applies the environment and decides which holder is configured. It
// refuses a configuration that names several, or one that cannot work.
func (e EncryptionConfig) Resolve(getenv func(string) string) (Resolved, error) {
	r := Resolved{KeyFile: e.KeyFile, PreviousKeyFile: e.PreviousKeyFile, Keystore: e.Keystore, PassphraseFile: e.PassphraseFile}
	if v := getenv(EnvRootKeyFile); v != "" {
		r.KeyFile = v
	}
	if v := getenv(EnvRootKeyPreviousFile); v != "" {
		r.PreviousKeyFile = v
	}
	if v := getenv(EnvPassphraseFile); v != "" {
		r.PassphraseFile = v
	}
	set := map[string]bool{
		"key_file":   r.KeyFile != "",
		"env":        strings.TrimSpace(getenv(EnvRootKey)) != "",
		"passphrase": r.Keystore != "",
		"transit":    e.Transit.Address != "",
	}
	var named []string
	for _, k := range []string{"key_file", "env", "passphrase", "transit"} {
		if set[k] {
			named = append(named, k)
		}
	}
	switch e.Holder {
	case "":
		switch len(named) {
		case 0:
		case 1:
			r.Kind = named[0]
		default:
			return r, fmt.Errorf("encryption: more than one root key holder is configured (%s); say which with encryption.holder, and remove the others", strings.Join(named, ", "))
		}
	case "key_file", "env", "passphrase", "transit":
		r.Kind = e.Holder
		if !set[e.Holder] {
			return r, fmt.Errorf("encryption.holder is %s but nothing configures it: %s", e.Holder, holderNeeds[e.Holder])
		}
	default:
		return r, fmt.Errorf("encryption.holder %q is not known; use key_file, env, passphrase or transit", e.Holder)
	}
	if r.Kind == "" && e.Require {
		return r, errors.New("encryption.require is set but no root key holder is configured: " + holderNeeds["key_file"])
	}
	return r, nil
}

var holderNeeds = map[string]string{
	"key_file":   "set encryption.key_file (or LAUNCHWAY_ROOT_KEY_FILE) to the key file; `launchway encryption init` makes one",
	"env":        "set the environment variable " + EnvRootKey,
	"passphrase": "set encryption.keystore_file; `launchway encryption init --passphrase` makes one",
	"transit":    "set encryption.transit.address, key and a login (token_env, token_file or role)",
}

// DefaultKeyFile is where a new installation generates its key file.
func DefaultKeyFile(dataDir string) string { return filepath.Join(dataDir, "root.key") }

// FileExists reports whether path exists.
func FileExists(path string) bool { _, err := os.Stat(path); return err == nil }
