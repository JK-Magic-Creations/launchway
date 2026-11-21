package storage

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// hookNames are the hooks git runs for a push. Each is a symlink to the
// launchway binary, which answers by the name it was started under (see
// gitexec.RegisterEntry): no shell, and no script in the repository.
var hookNames = []string{"pre-receive", "reference-transaction", "post-receive"}

// Environment that tells a hook which push it belongs to. A hook started
// without them (by anyone but a Receive of this process) does nothing.
const (
	envSocket = "LAUNCHWAY_HOOK_SOCK"
	envToken  = "LAUNCHWAY_HOOK_TOKEN" //nolint:gosec // the name of a variable, not a credential
)

// hookRequest and hookReply are the one-line JSON messages between a hook
// process and the server that is receiving the push.
type hookRequest struct {
	Token      string      `json:"token"`
	Hook       string      `json:"hook"`
	State      string      `json:"state,omitempty"`
	Updates    []RefUpdate `json:"updates,omitempty"`
	Quarantine string      `json:"quarantine,omitempty"`
	// Options are the push options the client sent (git push -o), read by
	// the post-receive hook from GIT_PUSH_OPTION_*.
	Options []string `json:"options,omitempty"`
}

type hookReply struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// ReceiveResult says what a push came to.
type ReceiveResult struct {
	// Committed is true when the database transaction that is the commit point
	// committed (even if the client was then not told: a push may report
	// failure though it is committed, and a retry says "up to date").
	Committed bool
	// Updates are the refs of the push.
	Updates []RefUpdate
	// Stale is set when the push lost a race: a ref was not where the pusher
	// said. Nothing was changed.
	Stale *ErrStale
	// Generation is the repository's generation after the commit.
	Generation int64
	// Options are the push options the client sent (git push -o).
	Options []string
}

// pushState is the server's record of one push, shared by its hooks.
type pushState struct {
	store  *Store
	repo   string
	token  string
	faults Faults
	mu     sync.Mutex
	packID string
	result ReceiveResult
	policy func(ctx context.Context, updates []RefUpdate) error
	notice func(ctx context.Context, updates []RefUpdate, options []string) string
}

// Receive serves one request of git's stateless receive-pack: the ref
// advertisement, or a push. The cache is made current first; a push holds the
// cache's guard shared for its whole run, and stale lock files of a killed git
// are removed before it starts. The hooks call back over a Unix socket that
// exists only for this call.
func (s *Store) Receive(ctx context.Context, repo string, rpc RPC) (ReceiveResult, error) {
	var res ReceiveResult
	if err := s.ensureCurrent(ctx, repo); err != nil {
		return res, err
	}
	g, err := s.guardFor(repo)
	if err != nil {
		return res, err
	}
	defer g.release()
	if !rpc.Advertise {
		// Whoever holds the guard exclusively knows no receive-pack runs here.
		if g.tryExclusive() {
			cleanStale(s.cacheDir(repo))
		}
		if err := g.shared(); err != nil {
			return res, err
		}
	}
	// -c here and not in the cache's configuration, so a cache made before push options existed offers them too.
	args := []string{"-c", "receive.advertisePushOptions=true", "receive-pack", "--stateless-rpc"}
	if rpc.Stream {
		args = []string{"-c", "receive.advertisePushOptions=true", "receive-pack"}
	} else if rpc.Advertise {
		args = append(args, "--advertise-refs")
	}
	args = append(args, ".")
	spec := gitexec.Spec{GitDir: s.cacheDir(repo), Args: args, Env: rpc.env(), Stdin: rpc.In, Stdout: rpc.Out,
		HooksPath: s.hooksDir(repo), IdleTimeout: 5 * time.Minute, Limits: s.o.Limits}
	if rpc.Advertise && !rpc.Stream {
		spec.HooksPath = ""
		_, err := gitexec.Run(ctx, spec)
		return res, err
	}
	st := &pushState{store: s, repo: repo, faults: orNone(faultsFrom(ctx)), token: randomID(), policy: rpc.Policy, notice: rpc.Notice}
	sock, stop, err := st.listen(ctx)
	if err != nil {
		return res, err
	}
	defer stop()
	spec.Env = append(spec.Env, envSocket+"="+sock, envToken+"="+st.token)
	before := s.readGen(repo)
	_, runErr := gitexec.Run(ctx, spec)
	st.mu.Lock()
	res = st.result
	pack := st.packID
	st.mu.Unlock()
	if runErr == nil && res.Committed && before >= 0 && res.Generation == before+1 {
		// This push was the only change since the cache was current, and git has
		// moved the cache's refs and kept the pack: the cache is current at the
		// new generation, with the pack's objects in it. Without this the next
		// request would download the pack it has just received.
		if pack != "" {
			_ = os.WriteFile(filepath.Join(s.cacheDir(repo), "launchway", "packs", pack), nil, 0o600)
		}
		_ = s.writeGen(repo, res.Generation)
	}
	if runErr != nil && res.Stale != nil {
		return res, res.Stale
	}
	return res, runErr
}

// listen opens the socket the hooks of this push talk to.
func (st *pushState) listen(ctx context.Context) (path string, stop func(), err error) {
	dir, err := os.MkdirTemp("", "lw-hook-")
	if err != nil {
		return "", nil, err
	}
	path = filepath.Join(dir, "s")
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	_ = os.Chmod(path, 0o600)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go st.serve(ctx, c)
		}
	}()
	return path, func() { ln.Close(); <-done; os.RemoveAll(dir) }, nil
}

func (st *pushState) serve(ctx context.Context, c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Minute))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return
	}
	var req hookRequest
	reply := hookReply{}
	if err := json.Unmarshal(line, &req); err != nil || subtle.ConstantTimeCompare([]byte(req.Token), []byte(st.token)) != 1 {
		reply.Message = "not allowed"
	} else {
		reply = st.handle(ctx, req)
	}
	data, _ := json.Marshal(reply)
	_, _ = c.Write(append(data, '\n'))
}

func (st *pushState) handle(ctx context.Context, req hookRequest) hookReply {
	fail := func(err error) hookReply { return hookReply{Message: err.Error()} }
	req.Updates = realRefUpdates(req.Updates)
	switch req.Hook {
	case "pre-receive":
		if err := st.faults.Hit(PtPreReceive); err != nil {
			return fail(err)
		}
		if st.policy != nil {
			if err := st.policy(st.withChangedFiles(ctx, req.Quarantine), req.Updates); err != nil {
				return fail(err)
			}
		}
		if err := st.preReceive(ctx, req); err != nil {
			return fail(err)
		}
	case "reference-transaction":
		switch req.State {
		case "prepared":
			if err := st.faults.Hit(PtPrepared); err != nil {
				return fail(err)
			}
			st.mu.Lock()
			pack := st.packID
			st.mu.Unlock()
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			gen, err := st.store.l.Commit(cctx, st.repo, req.Updates, pack, st.faults)
			st.mu.Lock()
			if gen > 0 {
				st.result.Committed, st.result.Generation = true, gen
				st.result.Updates = append(st.result.Updates, req.Updates...)
			}
			var stale *ErrStale
			if errors.As(err, &stale) {
				st.result.Stale = stale
			}
			st.mu.Unlock()
			if err != nil {
				return fail(err)
			}
		case "committed":
			if err := st.faults.Hit(PtCommitted); err != nil {
				return fail(err)
			}
		}
	case "post-receive":
		if err := st.faults.Hit(PtPostReceive); err != nil {
			return fail(err)
		}
		st.mu.Lock()
		st.result.Options = req.Options
		committed := st.result.Committed
		st.mu.Unlock()
		if st.notice != nil && committed {
			// What the server wants the pusher to read: git prints it as "remote: ...". It never fails the push.
			return hookReply{OK: true, Message: st.notice(ctx, req.Updates, req.Options)}
		}
	}
	return hookReply{OK: true}
}

// preReceive stores the pack git holds in quarantine, before any ref can move:
// upload, check, register as pending. A push that arrives as loose objects, or
// as more than one pack, is refused (receive.unpackLimit=1 makes every push one
// pack; the design depends on it). Quotas are checked here, with a plain
// message.
func (st *pushState) preReceive(ctx context.Context, req hookRequest) error {
	if req.Quarantine == "" {
		return nil
	}
	ents, _ := os.ReadDir(req.Quarantine)
	for _, e := range ents {
		if e.IsDir() && len(e.Name()) == 2 {
			return errors.New("the push arrived as loose objects, not as one pack; the server is misconfigured")
		}
	}
	packs, _ := filepath.Glob(filepath.Join(req.Quarantine, "pack", "*.pack"))
	switch {
	case len(packs) == 0:
		return nil // no new objects: a branch created at an existing commit, or a deletion
	case len(packs) > 1:
		return fmt.Errorf("the push arrived as %d packs, not one", len(packs))
	}
	o := st.store.o
	if fi, err := os.Stat(packs[0]); err == nil {
		if o.MaxPackBytes > 0 && fi.Size() > o.MaxPackBytes {
			return fmt.Errorf("this push is %s, more than the %s one push may be; split it into smaller pushes", mib(fi.Size()), mib(o.MaxPackBytes))
		}
		if o.MaxRepoBytes > 0 {
			u, err := st.store.l.Usage(ctx, st.repo)
			if err != nil {
				return err
			}
			if u.LiveBytes+u.PendingBytes+fi.Size() > o.MaxRepoBytes {
				return fmt.Errorf("this repository would be %s, more than the %s one repository may be", mib(u.LiveBytes+u.PendingBytes+fi.Size()), mib(o.MaxRepoBytes))
			}
		}
	}
	p, err := putPack(ctx, o.Blobs, st.repo, packs[0], !o.NoVerifyUpload)
	if err != nil {
		return fmt.Errorf("storing the pack: %w", err)
	}
	if err := st.faults.Hit(PtAfterUpload); err != nil {
		return err
	}
	if err := st.store.l.RegisterPack(ctx, st.repo, p); err != nil {
		return fmt.Errorf("registering the pack: %w", err)
	}
	if err := st.faults.Hit(PtAfterRegister); err != nil {
		return err
	}
	st.mu.Lock()
	st.packID = p.ID
	st.mu.Unlock()
	return nil
}

func mib(n int64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }

// Upload serves one request of git's stateless upload-pack: the ref
// advertisement, or a fetch or clone. The cache is made current first; the
// fetch itself holds no lock, so a slow clone never blocks a sync.
func (s *Store) Upload(ctx context.Context, repo string, rpc RPC) error {
	if err := s.ensureCurrent(ctx, repo); err != nil {
		return err
	}
	args := []string{"upload-pack", "--stateless-rpc"}
	if rpc.Stream {
		args = []string{"upload-pack"}
	} else if rpc.Advertise {
		args = append(args, "--advertise-refs")
	}
	args = append(args, ".")
	_, err := gitexec.Run(ctx, gitexec.Spec{GitDir: s.cacheDir(repo), Args: args, Env: rpc.env(), Stdin: rpc.In, Stdout: rpc.Out,
		IdleTimeout: 5 * time.Minute, Limits: s.o.Limits})
	return err
}

// ---------------------------------------------------------------- hooks

func init() { gitexec.RegisterEntry(hookEntry) }

// hookEntry runs when the launchway binary was started as one of git's hooks:
// it passes what git said to the server that is receiving the push, and exits
// with its answer. Started under any other name it returns at once.
func hookEntry() {
	name := filepath.Base(os.Args[0])
	known := false
	for _, n := range hookNames {
		known = known || n == name
	}
	if !known {
		return
	}
	sock, token := os.Getenv(envSocket), os.Getenv(envToken)
	if sock == "" || token == "" {
		fmt.Fprintln(os.Stderr, "launchway: this hook only runs for a push the server is receiving")
		os.Exit(1)
	}
	req := hookRequest{Token: token, Hook: name, Quarantine: os.Getenv("GIT_QUARANTINE_PATH")}
	if len(os.Args) > 1 {
		req.State = os.Args[1]
	}
	if n, _ := strconv.Atoi(os.Getenv("GIT_PUSH_OPTION_COUNT")); n > 0 && n <= 32 {
		for i := 0; i < n; i++ {
			req.Options = append(req.Options, os.Getenv("GIT_PUSH_OPTION_"+strconv.Itoa(i)))
		}
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 3 {
			req.Updates = append(req.Updates, RefUpdate{Old: f[0], New: f[1], Name: f[2]})
		}
	}
	dctx, cancelDial := context.WithTimeout(context.Background(), 10*time.Second)
	c, err := (&net.Dialer{}).DialContext(dctx, "unix", sock)
	cancelDial()
	if err != nil {
		fmt.Fprintf(os.Stderr, "launchway: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()
	data, _ := json.Marshal(req)
	if _, err := c.Write(append(data, '\n')); err != nil {
		os.Exit(1)
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "launchway: the server went away")
		os.Exit(1)
	}
	var reply hookReply
	if json.Unmarshal(line, &reply) == nil && reply.OK && reply.Message != "" {
		fmt.Fprintln(os.Stderr, reply.Message)
	}
	if json.Unmarshal(line, &reply) != nil || !reply.OK {
		if reply.Message != "" {
			fmt.Fprintf(os.Stderr, "launchway: %s\n", reply.Message)
		}
		os.Exit(1)
	}
	os.Exit(0)
}
