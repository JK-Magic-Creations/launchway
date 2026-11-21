package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

const encryptionUsage = `Usage:
  launchway encryption init   [--key-file PATH] [--config launchway.yaml]       make a root key file
  launchway encryption init   --passphrase [--keystore PATH] [--passphrase-file F]  make a passphrase keystore
  launchway encryption status [--json]                       a server's encryption at rest (administrators)
  launchway encryption rotate [--data] [--config launchway.yaml]   new key-encryption key; --data also re-seals every value
  launchway encryption rewrap [--config launchway.yaml]             replace the root key (old and new given together)

Encryption at rest keeps every secret the server stores sealed under a root key that is held outside the
database, so a copy of the database alone reveals none of them. init, rotate and rewrap run where the root
key is (the same configuration file or environment as the server) and open the database directly, so they
work while servers run. status asks a server, and so needs 'launchway login'. 'launchway doctor encryption'
reads the state from the server host without logging in.

Replacing the root key: make the new key (init), configure it as the root key with the old one as
encryption.previous_key_file, run rewrap, restart the servers, then remove the old key from the configuration.
See the guide "Encryption at rest".
`

func cmdEncryption(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, encryptionUsage)
		return cliErr{exitUsage, ""}
	}
	switch args[0] {
	case "init":
		return encryptionInit(args[1:])
	case "rotate":
		return encryptionRotate(args[1:])
	case "rewrap":
		return encryptionRewrap(args[1:])
	case "status":
		return runClient(func() error {
			c, err := loadCredentials()
			if err != nil {
				return err
			}
			return newClient(c, os.Stdout).encryptionStatus(args[1:])
		})
	}
	return usageErr("unknown command encryption %s\n\n%s", args[0], encryptionUsage)
}

func (cl *client) encryptionStatus(args []string) error {
	fs := flag.NewFlagSet("encryption status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var r sealing.Report
	if err := cl.call("GET", "/api/v1/encryption", nil, &r); err != nil {
		return err
	}
	return cl.print(r, *asJSON, func() { printEncryption(cl.out, r) })
}

// printEncryption writes a report in plain words.
func printEncryption(w io.Writer, r sealing.Report) {
	fmt.Fprintln(w, "Encryption at rest")
	fmt.Fprintf(w, "  %s\n", r.Summary)
	if r.Holder != nil {
		fmt.Fprintf(w, "  Root key holder ......... %s (%s)\n", r.Holder.Kind, r.Holder.Where)
	}
	if r.Keys != nil {
		fmt.Fprintf(w, "  Key versions ............ key-encryption key %d current, %d data keys\n", r.Keys.CurrentKEK, len(r.Keys.DataKeys))
		if r.Keys.OldestKeyMillis > 0 {
			fmt.Fprintf(w, "  Oldest key in use ....... %d days\n", int(time.Since(time.UnixMilli(r.Keys.OldestKeyMillis)).Hours()/24))
		}
	}
	fmt.Fprintf(w, "  Secrets in the database . %d sealed, %d in the clear\n", r.Sealed, r.Remaining)
	for _, c := range r.Columns {
		fmt.Fprintf(w, "    %-14s %-14s %d sealed, %d in the clear   (%s)\n", c.Table, c.Column, c.Sealed, c.Remaining, c.Why)
	}
	fmt.Fprintf(w, "  Sealing progress ........ %s\n", r.Migration.State)
	yn := map[bool]string{true: "yes", false: "NO"}
	fmt.Fprintf(w, "  A database dump alone cannot reveal secrets: %s\n", yn[r.DumpAloneIsUseless])
	for _, x := range r.Warnings {
		fmt.Fprintf(w, "  WARNING: %s\n", x)
	}
	for _, x := range r.Advice {
		fmt.Fprintf(w, "  Advice: %s\n", x)
	}
}

// localTool opens what a command that runs on the server host needs: the
// bootstrap file's settings, the root key holders and the database.
type localTool struct {
	cfg  *config.Config
	keys *rootKeys
	eng  *engine.Engine
}

func openLocalTool(cfgPath string) (*localTool, error) {
	cfg, err := config.LoadBootstrap(cfgPath)
	if err != nil {
		return nil, err
	}
	keys, err := openRootKeys(withDefaultKey(cfg), os.Getenv, promptPassphrase)
	if err != nil {
		return nil, err
	}
	return &localTool{cfg: cfg, keys: keys}, nil
}

func (l *localTool) openEngine() error {
	wait, _, err := l.cfg.Timings()
	if err != nil {
		return err
	}
	l.eng, err = engine.Open(engine.Options{Database: l.cfg.DatabaseAddr(), ConnectWait: wait, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return err
}

func (l *localTool) service(ctx context.Context) (*sealing.Service, error) {
	if l.keys.kind == "" {
		return nil, keyring.ErrNoRootKey
	}
	if err := l.openEngine(); err != nil {
		return nil, err
	}
	return sealing.Start(ctx, sealing.Options{DB: l.eng.Database(), Holder: l.keys.primary, Previous: l.keys.previous,
		Audit: auditEncryption(l.eng), Instance: instanceName() + "-cli"})
}

func (l *localTool) close() {
	if l.eng != nil {
		l.eng.Close()
	}
}

func encryptionInit(args []string) error {
	fs := flag.NewFlagSet("encryption init", flag.ContinueOnError)
	cfgPath := fs.String("config", "launchway.yaml", "the server's bootstrap file")
	keyFile := fs.String("key-file", "", "where to write the root key file (default: encryption.key_file, else <data>/root.key)")
	pass := fs.Bool("passphrase", false, "make a passphrase keystore instead of a key file")
	keystore := fs.String("keystore", "", "where to write the keystore (default: encryption.keystore_file, else <data>/root.keystore)")
	passFile := fs.String("passphrase-file", "", "read the new passphrase from this file (first line); else ask twice on the terminal")
	if err := fs.Parse(args); err != nil {
		return usageErr("%v", err)
	}
	cfg, err := config.LoadBootstrap(*cfgPath)
	if err != nil {
		return err
	}
	if *pass {
		path := firstNonEmpty(*keystore, cfg.Encryption.Keystore, cfg.Data+"/root.keystore")
		var p []byte
		if *passFile != "" {
			p, err = keyring.ReadPassphrase(*passFile)
		} else {
			p, err = askNewPassphrase()
		}
		if err != nil {
			return err
		}
		if err := keyring.CreateKeystore(path, p); err != nil {
			return err
		}
		keyring.Zero(p)
		fmt.Printf("Created the passphrase keystore %s.\n\n%s", path, afterInit("encryption.keystore_file: "+path+"\n  encryption.passphrase_file: <a file holding the passphrase>   (or start the server in a terminal to be asked)", "the keystore file AND the passphrase"))
		return nil
	}
	path := firstNonEmpty(*keyFile, os.Getenv(config.EnvRootKeyFile), cfg.Encryption.KeyFile, config.DefaultKeyFile(cfg.Data))
	if err := keyring.WriteKeyFile(path); err != nil {
		return err
	}
	fmt.Printf("Created the root key file %s (mode 0600).\n\n%s", path, afterInit("encryption.key_file: "+path+"\n  (or the environment variable LAUNCHWAY_ROOT_KEY_FILE="+path+")", "the key file"))
	return nil
}

func afterInit(setting, what string) string {
	return "What to do now:\n" +
		"  1. Keep a copy of " + what + " somewhere other than the database's backups (a password manager, a sealed printout in a safe).\n" +
		"     Without it a restore cannot read any secret, and nobody, including the Launchway project, can recover it for you.\n" +
		"  2. Tell every server where it is, then restart them:\n  " + setting + "\n" +
		"     On the first start with the key, the secrets already stored are sealed in place; the server says when it is done.\n" +
		"  3. Check with: launchway doctor encryption\n"
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func askNewPassphrase() ([]byte, error) {
	a, err := promptPassphrase()
	if err != nil {
		return nil, err
	}
	fmt.Fprint(os.Stderr, "Again: ")
	b, err := promptPassphraseNoLabel()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(a, b) {
		return nil, errors.New("the two passphrases differ")
	}
	return a, nil
}

func encryptionRotate(args []string) error {
	fs := flag.NewFlagSet("encryption rotate", flag.ContinueOnError)
	cfgPath := fs.String("config", "launchway.yaml", "the server's bootstrap file")
	data := fs.Bool("data", false, "also make new data keys and re-seal every stored value under them")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return usageErr("%v", err)
	}
	l, err := openLocalTool(*cfgPath)
	if err != nil {
		return err
	}
	defer l.close()
	svc, err := l.service(context.Background())
	if err != nil {
		return err
	}
	res, err := svc.Rotate(context.Background(), *data)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(res)
	}
	fmt.Printf("Key-encryption key %d is now current; %d data keys were re-wrapped under it, %d old key-encryption keys retired.\n", res.Keys.KEKVersion, res.Keys.Rewrapped, res.Keys.Retired)
	if *data {
		fmt.Printf("%d new data key versions were made and %d stored values re-sealed under them.\n", res.DataKeys, res.Resealed)
	}
	fmt.Println("Servers keep running: nothing a request reads or writes was blocked. The root key itself is unchanged (see 'launchway encryption rewrap' to replace it).")
	return nil
}

func encryptionRewrap(args []string) error {
	fs := flag.NewFlagSet("encryption rewrap", flag.ContinueOnError)
	cfgPath := fs.String("config", "launchway.yaml", "the server's bootstrap file")
	if err := fs.Parse(args); err != nil {
		return usageErr("%v", err)
	}
	l, err := openLocalTool(*cfgPath)
	if err != nil {
		return err
	}
	defer l.close()
	if len(l.keys.previous) == 0 {
		return usageErr("give the root key being replaced beside the new one: encryption.previous_key_file (or LAUNCHWAY_ROOT_KEY_PREVIOUS_FILE); the configured root key is the NEW one")
	}
	svc, err := l.service(context.Background())
	if err != nil {
		return err
	}
	n, err := svc.Rewrap(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("%d key-encryption keys are now wrapped by the configured %s root key. No value and no data key was touched.\n", n, l.keys.primary.Kind())
	fmt.Println("Next: restart the servers with the new key alone (remove encryption.previous_key_file), keep a copy of the new key, and destroy the old one when no backup needs it.")
	return nil
}

// promptPassphraseNoLabel reads a passphrase after the caller printed the label.
func promptPassphraseNoLabel() ([]byte, error) {
	p, err := promptPassphraseQuiet()
	return []byte(strings.TrimRight(string(p), "\r\n")), err
}
