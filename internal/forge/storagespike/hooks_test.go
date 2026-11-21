package storagespike

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// HookMain must be called first by the TestMain of any package that runs
// pushes through this spike. When the binary was started as one of git's
// hooks (a symlink named pre-receive, reference-transaction or post-receive)
// or as the receive-pack wrapper, it does that work and exits; otherwise it
// returns at once.
func HookMain() {
	name := filepath.Base(os.Args[0])
	known := false
	for _, n := range hookNames {
		known = known || n == name
	}
	if !known {
		return
	}
	ctx := context.Background()
	var err error
	switch name {
	case "receive-pack-wrapper":
		err = wrapperMain(ctx)
	case "pre-receive":
		err = preReceive(ctx)
	case "reference-transaction":
		err = referenceTransaction(ctx)
	case "post-receive":
		err = postReceive(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "launchway: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// killFault is Fault.Hit with the whole push taken down on "kill": the client
// git and the wrapper are in the group KillPGEnv names.
func hit(f Fault, point string) error {
	if f.Point == point && f.Action == "kill" {
		if pg, err := strconv.Atoi(os.Getenv(KillPGEnv)); err == nil && pg > 1 {
			_ = syscall.Kill(-pg, syscall.SIGKILL)
		}
	}
	return f.Hit(point)
}

// pushState is what pre-receive hands to reference-transaction: the pack it
// uploaded.
type pushState struct {
	PackID string `json:"pack_id"`
}

func statePath(cfg Config) string {
	return filepath.Join(cfg.Cache, "launchway", fmt.Sprintf("push-%d.json", os.Getppid()))
}

func readUpdates(r io.Reader) []RefUpdate {
	var ups []RefUpdate
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// Git before about 2.50 adds a line for HEAD when the branch it points at is
		// updated; HEAD is not a ref of the repository (see storage.realRefUpdates).
		if len(f) == 3 && strings.HasPrefix(f[2], "refs/") {
			ups = append(ups, RefUpdate{Old: f[0], New: f[1], Name: f[2]})
		}
	}
	return ups
}

// quarantinePacks lists the packs of the push still held in git's quarantine
// directory, and refuses loose objects: with receive.unpackLimit=1 every push
// arrives as one pack, and the design depends on it.
func quarantinePacks() ([]string, error) {
	q := os.Getenv("GIT_QUARANTINE_PATH")
	if q == "" {
		return nil, nil
	}
	packs, _ := filepath.Glob(filepath.Join(q, "pack", "*.pack"))
	ents, _ := os.ReadDir(q)
	for _, e := range ents {
		if e.IsDir() && len(e.Name()) == 2 {
			return nil, errors.New("the push arrived as loose objects, not as one pack; refusing (receive.unpackLimit must be 1)")
		}
	}
	return packs, nil
}

func preReceive(ctx context.Context) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	f := FromEnv()
	if err := hit(f, PtPreReceive); err != nil {
		return err
	}
	if cfg.Mode != "ledger" {
		return nil
	}
	packs, err := quarantinePacks()
	if err != nil {
		return err
	}
	if len(packs) > 1 {
		return fmt.Errorf("the push arrived as %d packs, not one", len(packs))
	}
	if len(packs) == 0 {
		return os.WriteFile(statePath(cfg), []byte("{}"), 0o644) // a push with no new objects
	}
	r, err := OpenReplica(cfg, f)
	if err != nil {
		return err
	}
	defer r.Close()
	info, err := PutPack(ctx, r.Blob, cfg.RepoID, packs[0], cfg.VerifyUpload)
	if err != nil {
		return fmt.Errorf("storing the pack: %w", err)
	}
	if err := hit(f, PtAfterUpload); err != nil {
		return err
	}
	if err := r.L.RegisterPack(ctx, cfg.RepoID, info); err != nil {
		return fmt.Errorf("registering the pack: %w", err)
	}
	if err := hit(f, PtAfterRegister); err != nil {
		return err
	}
	data, _ := json.Marshal(pushState{PackID: info.ID})
	return os.WriteFile(statePath(cfg), data, 0o644)
}

func referenceTransaction(ctx context.Context) error {
	if len(os.Args) < 2 {
		return nil
	}
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	f := FromEnv()
	state := os.Args[1]
	ups := readUpdates(os.Stdin)
	switch state {
	case "prepared":
		if err := hit(f, PtPrepared); err != nil {
			return err
		}
		if cfg.Mode != "ledger" {
			return nil
		}
		var st pushState
		if data, err := os.ReadFile(statePath(cfg)); err == nil {
			_ = json.Unmarshal(data, &st)
		} else if len(ups) > 0 {
			return errors.New("no record of this push (pre-receive did not run); refusing")
		}
		r, err := OpenReplica(cfg, f)
		if err != nil {
			return err
		}
		defer r.Close()
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		_, err = r.L.Commit(cctx, cfg.RepoID, ups, st.PackID)
		var stale *ErrStale
		if errors.As(err, &stale) {
			fmt.Fprintf(os.Stderr, "launchway: rejected, %v; fetch and try again\n", stale)
		}
		return err
	case "committed":
		return hit(f, PtCommitted)
	}
	return nil
}

func postReceive(ctx context.Context) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	f := FromEnv()
	ups := readUpdates(os.Stdin)
	_ = os.Remove(statePath(cfg))
	if err := hit(f, PtPostReceive); err != nil {
		return err
	}
	if cfg.Mode != "volume" {
		return nil
	}
	// Candidate A: the audit ledger is written after the fact. Failing here
	// never undoes the push; the poller notices a ref the ledger lacks.
	if f.Is("ledger", "error") {
		return nil
	}
	l, err := OpenLedger(cfg.DBAddr)
	if err != nil {
		return nil
	}
	defer l.Close()
	for _, u := range ups {
		_ = l.AppendLedger(ctx, cfg.RepoID, u)
	}
	return nil
}

// wrapperMain is what the client's git runs as "git receive-pack": it makes
// this server current (candidate B), then runs the real receive-pack, with
// the hooks of this server, passing the client's bytes through. Like the
// smart-HTTP handler it stands for, it runs git through gitexec.
func wrapperMain(ctx context.Context) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	if len(os.Args) < 2 {
		return errors.New("no repository given")
	}
	f := FromEnv()
	env := []string{CfgEnv + "=" + os.Getenv(CfgEnv), KillPGEnv + "=" + strconv.Itoa(syscall.Getpgrp())}
	env = append(env, f.Env()...)
	hooks := filepath.Join(cfg.Cache, "launchway", "hooks")
	g, err := openGuard(filepath.Join(cfg.Cache, "launchway"))
	if err != nil {
		return err
	}
	defer g.release()
	if cfg.Mode == "ledger" {
		// Exclusive while this server is made current: no receive-pack runs,
		// so every lock file is garbage from a git that was killed.
		if err := g.exclusive(); err != nil {
			return err
		}
		r, err := OpenReplica(cfg, Fault{}) // the sync itself is not what is being attacked here
		if err != nil {
			return err
		}
		err = r.syncLocked(ctx)
		r.Close()
		if err != nil {
			return fmt.Errorf("making this server current: %w", err)
		}
	} else if g.tryExclusive() {
		CleanStale(cfg.Cache)
	}
	// Held shared for the whole receive-pack: it is how the next push knows a
	// lock file is not in use.
	if err := g.shared(); err != nil {
		return err
	}
	spec := gitexec.Spec{GitDir: cfg.Cache, Args: []string{"receive-pack", cfg.Cache}, HooksPath: hooks, Env: env,
		Stdin: os.Stdin, Stdout: os.Stdout, Timeout: 20 * time.Minute}
	if cfg.Mode == "volume" && cfg.Fsync != "" {
		spec.Args = append([]string{"-c", "core.fsync=" + cfg.Fsync}, spec.Args...)
	}
	res, err := gitexec.Run(ctx, spec)
	if res.Stderr != "" {
		fmt.Fprintln(os.Stderr, res.Stderr)
	}
	return err
}
