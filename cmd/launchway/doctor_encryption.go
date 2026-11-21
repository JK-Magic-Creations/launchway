package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

// doctorEncryption reports encryption at rest from the server host: which
// holder keeps the root key, whether it opens the database, how many stored
// secrets are sealed, and whether a copy of the database alone could reveal
// one. It reads and changes nothing; --restore-drill also opens every sealed
// secret to prove the key and the database belong together.
func doctorEncryption(args []string) error {
	fs := flag.NewFlagSet("doctor encryption", flag.ExitOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	asJSON := fs.Bool("json", false, "print JSON")
	drill := fs.Bool("restore-drill", false, "open every sealed secret, proving this root key and this database belong together (reads only)")
	_ = fs.Parse(args)
	cfg, err := config.LoadBootstrap(*cfgPath)
	if err != nil {
		return err
	}
	wait, _, err := cfg.Timings()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait+2*time.Minute)
	defer cancel()
	rk, err := openRootKeys(ctx, withDefaultKey(cfg), os.Getenv, promptPassphrase)
	if err != nil {
		return fmt.Errorf("the root key cannot be used: %w", err)
	}
	db, err := engine.OpenInspected(cfg.DatabaseAddr(), wait) //nolint:contextcheck // connecting has its own timeouts
	if err != nil {
		return fmt.Errorf("the database cannot be reached, so nothing can be said about encryption: %w", err)
	}
	defer db.Close()
	svc, err := sealing.Inspect(ctx, sealing.Options{DB: db, Holder: rk.primary, Previous: rk.previous})
	if err != nil {
		if errors.Is(err, sealing.ErrDatabaseEncrypted) {
			fmt.Println("Encryption at rest: this database is encrypted, but no root key is configured here.")
			fmt.Println("  A database dump alone cannot reveal secrets: yes (it is useless without the key)")
		}
		return err
	}
	rep, err := svc.Report(ctx)
	if err != nil {
		return err
	}
	var dr *sealing.Drill
	if *drill {
		d, err := svc.VerifyAll(ctx)
		if err != nil {
			return fmt.Errorf("the restore drill needs a root key that opens this database: %w", err)
		}
		dr = &d
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"encryption": rep, "restore_drill": dr})
	}
	printEncryption(os.Stdout, rep)
	if dr != nil {
		fmt.Printf("  Restore drill ........... %d sealed secrets opened, %d failed\n", dr.Opened, dr.Failed)
		for _, e := range dr.Errors {
			fmt.Println("    " + e)
		}
		if dr.Failed > 0 {
			return exitErr(1)
		}
	}
	if rep.Remaining > 0 || !rep.Encrypted {
		return exitErr(1)
	}
	return nil
}

// encryptionBrief is the encryption lines of the plain `launchway doctor`:
// the one-sentence state, with the warnings, never an error that hides the
// rest of the report.
func encryptionBrief(ctx context.Context, cfg *config.Config, wait time.Duration) []string {
	rk, err := openRootKeys(ctx, withDefaultKey(cfg), os.Getenv, nil)
	if err != nil {
		return []string{"Encryption at rest: the root key cannot be used: " + err.Error()}
	}
	db, err := engine.OpenInspected(cfg.DatabaseAddr(), wait) //nolint:contextcheck // connecting has its own timeouts
	if err != nil {
		return []string{"Encryption at rest: not checked, the database cannot be reached"}
	}
	defer db.Close()
	svc, err := sealing.Inspect(ctx, sealing.Options{DB: db, Holder: rk.primary, Previous: rk.previous})
	if err != nil {
		return []string{"Encryption at rest: " + err.Error()}
	}
	rep, err := svc.Report(ctx)
	if err != nil {
		return []string{"Encryption at rest: " + err.Error()}
	}
	out := []string{"Encryption at rest: " + rep.Summary}
	for _, w := range rep.Warnings {
		if w != rep.Summary {
			out = append(out, "  WARNING: "+w)
		}
	}
	return append(out, "  (launchway doctor encryption has the details; artifacts, logs, caches and Git packs are not yet encrypted by Launchway)")
}
