package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/keyring/transit"
	"github.com/jk-magic-creations/launchway/internal/sealing"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// rootKeys is the root key holders a configuration names: the one in use and,
// during a root key replacement, the one being replaced.
type rootKeys struct {
	kind     string // "" when none is configured
	primary  keyring.Holder
	previous []keyring.Holder
	resolved config.Resolved
}

// openRootKeys opens the holders the configuration and environment name. It
// checks files and the transit settings; it does not contact the service.
// prompt reads a passphrase when the keystore has no passphrase file.
func openRootKeys(ctx context.Context, e config.EncryptionConfig, getenv func(string) string, prompt func() ([]byte, error)) (*rootKeys, error) {
	e = e.Overlay(getenv)
	r, err := e.Resolve(getenv)
	if err != nil {
		return nil, err
	}
	rk := &rootKeys{kind: r.Kind, resolved: r}
	switch r.Kind {
	case "":
		return rk, nil
	case "key_file":
		rk.primary, err = keyring.OpenKeyFile(r.KeyFile)
	case "env":
		rk.primary, err = keyring.OpenEnv(config.EnvRootKey, getenv)
	case "passphrase":
		var pass []byte
		if r.PassphraseFile != "" {
			pass, err = keyring.ReadPassphrase(r.PassphraseFile)
		} else if prompt != nil {
			pass, err = prompt()
		} else {
			err = errors.New("the keystore needs its passphrase: set encryption.passphrase_file (or LAUNCHWAY_KEYSTORE_PASSPHRASE_FILE), or start the server in a terminal to be asked")
		}
		if err == nil {
			rk.primary, err = keyring.OpenKeystore(ctx, r.Keystore, pass)
			keyring.Zero(pass)
		}
	case "transit":
		t := e.Transit
		rk.primary, err = transit.Open(transit.Config{StoreConfig: secrets.StoreConfig{Address: t.Address, Namespace: t.Namespace, CAFile: t.CAFile,
			TokenEnv: t.TokenEnv, TokenFile: t.TokenFile, Role: t.Role, AuthPath: t.AuthPath, JWTFile: t.JWTFile}, TransitMount: t.Mount, TransitKey: t.Key})
	}
	if err != nil {
		return nil, fmt.Errorf("encryption (%s): %w", r.Kind, err)
	}
	if r.PreviousKeyFile != "" {
		h, err := keyring.OpenKeyFile(r.PreviousKeyFile)
		if err != nil {
			return nil, fmt.Errorf("encryption.previous_key_file: %w", err)
		}
		rk.previous = append(rk.previous, h)
	}
	if v := getenv(config.EnvRootKeyPrevious); strings.TrimSpace(v) != "" {
		h, err := keyring.OpenEnv(config.EnvRootKeyPrevious, getenv)
		if err != nil {
			return nil, err
		}
		rk.previous = append(rk.previous, h)
	}
	return rk, nil
}

// withDefaultKey makes the root key file in the data directory the configured
// one when nothing else is (see defaultKeyFile), for the commands that run on
// the server host.
func withDefaultKey(cfg *config.Config) config.EncryptionConfig {
	e := cfg.Encryption.Overlay(os.Getenv)
	if r, err := e.Resolve(os.Getenv); err == nil && r.Kind == "" && config.FileExists(config.DefaultKeyFile(cfg.Data)) {
		e.KeyFile = config.DefaultKeyFile(cfg.Data)
	}
	return e
}

// promptPassphrase asks for the keystore's passphrase on the terminal.
func promptPassphrase() ([]byte, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // a file descriptor
		return nil, errors.New("the keystore needs its passphrase: set encryption.passphrase_file (or LAUNCHWAY_KEYSTORE_PASSPHRASE_FILE); there is no terminal to ask on")
	}
	fmt.Fprint(os.Stderr, "Passphrase for the Launchway keystore: ")
	return promptPassphraseQuiet()
}

// promptPassphraseQuiet reads a line from the terminal without echo.
func promptPassphraseQuiet() ([]byte, error) {
	b, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // a file descriptor
	fmt.Fprintln(os.Stderr)
	return b, err
}

// auditEncryption records what the keyring did (key creation, rotation, a
// failed unseal). The entries name keys by version, never hold key material.
func auditEncryption(eng *engine.Engine) func(context.Context, keyring.Event) {
	return func(_ context.Context, ev keyring.Event) {
		_ = eng.Audit(engine.AuditEntry{At: time.Now().UnixMilli(), User: "system", Source: "server", Action: ev.Action,
			Target: ev.Target, Outcome: ev.Outcome, Detail: ev.Detail})
	}
}

// startSecrets unlocks the root key and seals what was stored in the clear.
// With no root key: a brand-new installation generates a key file (unless
// told not to); an existing one starts "not encrypted at rest" and says so.
// A key that does not open the database, or no key for an encrypted database,
// refuses to start.
func (su *serverSetup) startSecrets(ctx context.Context, eng *engine.Engine) (*sealing.Service, error) {
	cfg, log := su.cfg, su.log
	rk, err := openRootKeys(ctx, withDefaultKey(cfg), os.Getenv, promptPassphrase)
	if err != nil {
		return nil, err
	}
	o := sealing.Options{DB: eng.Database(), Log: log, Audit: auditEncryption(eng), Instance: instanceName()}
	if rk.kind == "" {
		if rk, err = defaultKeyFile(ctx, cfg, eng, log); err != nil {
			return nil, err
		}
	}
	o.Holder, o.Previous = rk.primary, rk.previous
	svc, err := sealing.Start(ctx, o)
	if err != nil {
		return nil, err
	}
	if !svc.Encrypted() {
		log.Warn("NOT ENCRYPTED AT REST: no root key is configured, so the signing keys and the shared runner token are stored in the clear in the database, and a copy of the database reveals them. Git over SSH and webhook secrets stay off until a root key exists. " + keyring.HowToCreateARootKey)
	} else if rk.kind != "" {
		log.Info("encryption at rest is on", "root_key_holder", rk.primary.Kind())
	}
	return svc, nil
}

func instanceName() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// defaultKeyFile is what happens when no root key holder is configured. The
// key file in the data directory (<data>/root.key) is used if it is there: it
// is the one a new installation generated at its first start. If it is not,
// an installation that has never held a secret (a new database) generates it,
// so that it is encrypted at rest from its first start, unless told not to.
// An installation that already holds secrets is not touched: it runs not
// encrypted until an operator creates a key on purpose.
func defaultKeyFile(ctx context.Context, cfg *config.Config, eng *engine.Engine, log *slog.Logger) (*rootKeys, error) {
	none := &rootKeys{}
	path := config.DefaultKeyFile(cfg.Data)
	if !config.FileExists(path) {
		if cfg.Encryption.NoGenerate {
			return none, nil
		}
		fresh, err := sealing.IsFresh(ctx, eng.Database())
		if err != nil {
			return nil, err
		}
		if !fresh {
			return none, nil
		}
		if err := keyring.WriteKeyFile(path); err != nil {
			return nil, fmt.Errorf("making a root key for this new installation: %w", err)
		}
		log.Warn("a root key was generated for this new installation, because none was configured. Keep a copy of it somewhere other than the database's backups: without it a restore cannot read any secret. For production, move it off this volume and set encryption.key_file", "key_file", path)
	} else {
		log.Info("using the root key file in the data directory; for production, keep the key off this volume and set encryption.key_file", "key_file", path)
	}
	h, err := keyring.OpenKeyFile(path)
	if err != nil {
		return nil, err
	}
	return &rootKeys{kind: "key_file", primary: h}, nil
}

// secretsAndToken unlocks the root key and then reads the runner token the
// servers share, which is kept sealed once a root key is in use.
func (su *serverSetup) secretsAndToken(eng *engine.Engine) (*sealing.Service, string, error) {
	svc, err := su.startSecrets(context.Background(), eng)
	if err != nil {
		return nil, "", err
	}
	token, err := config.RunnerToken(su.cfg.Data, svc)
	return svc, token, err
}
