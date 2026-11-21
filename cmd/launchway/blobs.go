package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

const blobsUsage = `Usage:
  launchway blobs status [--config launchway.yaml]
      say which stores the database records artifacts in, and how much

  launchway blobs copy --from STORE --to STORE [--config launchway.yaml]
                       [--from-dir DIR] [--to-dir DIR] [--s3 s3.yaml]
                       [--delete-source] [--dry-run] [--json]
      copy every stored artifact file not yet recorded in the target store
      from the source store to the target, checking each file's SHA-256 as
      it is read and again after it is written, and record it there. Servers
      may keep running. Safe to stop and run again: it carries on where it
      was, and picks up what servers still on the old store kept meanwhile.

STORE is database, filesystem or s3:
  database    the database in the configuration file (or LAUNCHWAY_DATABASE)
  filesystem  --from-dir/--to-dir, else artifacts.dir, else <data>/artifacts
  s3          the s3: settings in --s3 FILE (the same fields as artifacts.s3),
              else artifacts.s3 in the configuration file; credentials from
              AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY (or their _FILE forms)

Moving to another store, with servers running:
  1. launchway blobs copy --from OLD --to NEW
  2. set artifacts.store to NEW (on PostgreSQL, unset means database) and
     roll the servers
  3. launchway blobs copy --from OLD --to NEW again, for what servers still
     on OLD kept during the roll
  4. optionally, once no server uses OLD: the same with --delete-source
`

func cmdBlobs(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, blobsUsage)
		return exitErr(2)
	}
	switch args[0] {
	case "status":
		return blobsStatus(args[1:])
	case "copy":
		return blobsCopy(args[1:])
	case "help", "-h", "--help":
		fmt.Print(blobsUsage)
		return nil
	}
	fmt.Fprintf(os.Stderr, "unknown blobs command %q\n\n%s", args[0], blobsUsage)
	return exitErr(2)
}

// openAdmin reads the configuration and opens the database for blob work.
func openAdmin(cfgPath string) (*config.Config, *engine.BlobAdmin, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if _, err := cfg.ArtifactLimits(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", cfgPath, err)
	}
	wait, _, err := cfg.Timings()
	if err != nil {
		return nil, nil, err
	}
	admin, err := engine.OpenBlobAdmin(cfg.DatabaseAddr(), wait)
	if err != nil {
		return nil, nil, err
	}
	return cfg, admin, nil
}

func blobsStatus(args []string) error {
	fs := flag.NewFlagSet("blobs status", flag.ExitOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	fs.Parse(args)
	cfg, admin, err := openAdmin(*cfgPath)
	if err != nil {
		return err
	}
	defer admin.Close()
	ctx := context.Background()
	counts, err := admin.Counts(ctx)
	if err != nil {
		return err
	}
	if len(counts) == 0 {
		fmt.Println("No artifact files are kept.")
	}
	for _, c := range counts {
		where := c.Store + " store"
		if c.Store == "" {
			where = "store not recorded (kept before schema 17; by default the filesystem)"
		}
		fmt.Printf("%8d files  %10s  %s\n", c.Files, sizeOf(c.Bytes), where)
	}
	if n, err := admin.Uploading(ctx); err == nil && n > 0 {
		fmt.Printf("%8d files being uploaded\n", n)
	}
	o, _ := cfg.ArtifactLimits()
	if db, err := admin.DatabaseStore(o.DatabaseLimits); err == nil {
		if u, err := db.Usage(ctx); err == nil {
			fmt.Printf("\nThe database store holds %s of its limit of %s (%d objects, %d uploads open, %s waiting to be collected).\n",
				sizeOf(u.UsedBytes), sizeOf(u.LimitBytes), u.Objects, u.OpenUploads, sizeOf(u.CollectBytes))
		}
	}
	return nil
}

func blobsCopy(args []string) error {
	fs := flag.NewFlagSet("blobs copy", flag.ExitOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	from := fs.String("from", "", "the store to copy from: database, filesystem or s3")
	to := fs.String("to", "", "the store to copy to: database, filesystem or s3")
	fromDir := fs.String("from-dir", "", "the filesystem store's directory, when copying from it")
	toDir := fs.String("to-dir", "", "the filesystem store's directory, when copying to it")
	s3File := fs.String("s3", "", "a YAML file with the S3 store's settings (endpoint, bucket, region, prefix, path_style, ca_file)")
	deleteSource := fs.Bool("delete-source", false, "delete each file from the source once it is recorded in the target (only once no server uses the source)")
	dryRun := fs.Bool("dry-run", false, "list what would be copied")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Parse(args)
	if *from == "" || *to == "" {
		fmt.Fprint(os.Stderr, blobsUsage)
		return exitErr(2)
	}
	cfg, admin, err := openAdmin(*cfgPath)
	if err != nil {
		return err
	}
	defer admin.Close()
	src, err := blobStoreFor(cfg, admin, *from, *fromDir, *s3File)
	if err != nil {
		return fmt.Errorf("--from %s: %w", *from, err)
	}
	dst, err := blobStoreFor(cfg, admin, *to, *toDir, *s3File)
	if err != nil {
		return fmt.Errorf("--to %s: %w", *to, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	err = dst.Check(cctx)
	cancel()
	if err != nil {
		return fmt.Errorf("the %s store does not work: %w", dst.Name(), err)
	}
	start := time.Now()
	progress := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	rep, err := admin.Copy(ctx, src, dst, engine.CopyOptions{DeleteSource: *deleteSource, DryRun: *dryRun, Progress: progress})
	if rep != nil {
		if *asJSON {
			b, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Println(string(b))
		} else {
			printCopyReport(rep, src.Name(), dst.Name(), time.Since(start), *dryRun)
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return errors.New("stopped; run the same command again to carry on")
		}
		return err
	}
	if rep.Failed > 0 {
		return exitErr(1)
	}
	return nil
}

func printCopyReport(rep *engine.CopyReport, from, to string, took time.Duration, dry bool) {
	if dry {
		fmt.Printf("Would copy %d files (%s) from the %s store to the %s store.\n", rep.Copied, sizeOf(rep.Bytes), from, to)
		return
	}
	rate := ""
	if s := took.Seconds(); s > 0 && rep.Bytes > 0 {
		rate = fmt.Sprintf(", %.1f MB/s", float64(rep.Bytes)/1e6/s)
	}
	fmt.Printf("Copied %d files (%s) from the %s store to the %s store in %s%s; %d were there already.\n",
		rep.Copied, sizeOf(rep.Bytes), from, to, took.Round(time.Second), rate, rep.Present)
	if rep.Missing > 0 {
		fmt.Printf("%d files are not in the %s store (kept in another store; copy from that one too).\n", rep.Missing, from)
	}
	if rep.Vanished > 0 {
		fmt.Printf("%d files were removed while they were copied (their retention ended); nothing to do.\n", rep.Vanished)
	}
	if rep.Failed > 0 {
		fmt.Printf("%d files FAILED:\n", rep.Failed)
		for _, e := range rep.Errors {
			fmt.Println("  " + e)
		}
	}
	if rep.Uploading > 0 {
		fmt.Printf("%d files are still being uploaded; run the copy again once their Sorties are done.\n", rep.Uploading)
	}
	if len(rep.Left) > 0 {
		fmt.Printf("Still recorded elsewhere: %s.\n", engine.DescribeElsewhere(rep.Left))
	} else {
		fmt.Printf("Every kept file is now recorded in the %s store.\n", to)
	}
}

// blobStoreFor makes a store named on the command line.
func blobStoreFor(cfg *config.Config, admin *engine.BlobAdmin, name, dir, s3File string) (blobstore.Store, error) {
	switch name {
	case "database":
		o, _ := cfg.ArtifactLimits()
		return admin.DatabaseStore(o.DatabaseLimits)
	case "filesystem":
		if dir == "" {
			dir = cfg.Artifacts.Dir
		}
		if dir == "" {
			dir = filepath.Join(cfg.Data, "artifacts")
		}
		if _, err := os.Stat(dir); err != nil {
			return nil, err
		}
		return blobstore.NewFilesystem(dir)
	case "s3":
		s3cfg := cfg.Artifacts.S3
		if s3File != "" {
			b, err := os.ReadFile(s3File)
			if err != nil {
				return nil, err
			}
			var c blobstore.S3Config
			dec := yaml.NewDecoder(bytes.NewReader(b))
			dec.KnownFields(true)
			if err := dec.Decode(&c); err != nil {
				return nil, fmt.Errorf("%s: %w", s3File, err)
			}
			s3cfg = &c
		}
		if s3cfg == nil {
			return nil, errors.New("no S3 settings: give --s3 FILE, or set artifacts.s3 in the configuration")
		}
		creds, err := blobstore.CredentialsFromEnv()
		if err != nil {
			return nil, err
		}
		return blobstore.NewS3(*s3cfg, creds)
	}
	return nil, fmt.Errorf("a store is database, filesystem or s3, not %q", name)
}

func sizeOf(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
