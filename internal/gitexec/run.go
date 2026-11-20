package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/redact"
)

// Errors a run can end with. Test for them with errors.Is.
var (
	// ErrTimeout means the total timeout of the Spec passed.
	ErrTimeout = errors.New("git took longer than its time limit")
	// ErrIdle means git wrote and read nothing for the idle timeout.
	ErrIdle = errors.New("git stopped making progress")
	// ErrOutputLimit means git wrote more standard output than allowed.
	ErrOutputLimit = errors.New("git wrote more output than allowed")
	// ErrDenied means the Spec asked for something the runner never allows:
	// an environment variable that is not on the allow-list, an argument
	// that is not valid, a repository with alternates.
	ErrDenied = errors.New("not allowed")
)

// Defaults for a Spec that does not say.
const (
	// DefaultMaxOutput is how much standard output Run captures when the
	// Spec has no Stdout of its own.
	DefaultMaxOutput = 256 << 20
	// MaxStderr is how much standard error Run keeps; the rest is dropped.
	MaxStderr = 1 << 20
	// killGrace is how long Run waits for the pipes of a killed process to
	// drain before it gives up on them.
	killGrace = 5 * time.Second
)

// Inherit says which of the server's own environment variables a git command
// may see.
type Inherit int

const (
	// Local passes only PATH, TMPDIR and TZ. Use it for commands that read
	// and write the local repository.
	Local Inherit = iota
	// Remote also passes what talking to a remote needs: Git settings given
	// as GIT_CONFIG_COUNT, GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n (how
	// credentials are given to Launchway), GIT_SSH, SSH_AUTH_SOCK,
	// certificate locations and proxy settings.
	Remote
)

// Spec describes one git invocation.
type Spec struct {
	// Dir is the working directory; empty means the process's own. GitDir,
	// when set, is the repository (GIT_DIR) and Dir defaults to it.
	Dir    string
	GitDir string
	// Args are git's arguments, subcommand first. They are passed as they
	// are: there is no shell.
	Args []string
	// Stdin is fed to git; nil means no input.
	Stdin io.Reader
	// Stdout receives git's standard output as it is written. When nil the
	// output is captured into Result.Stdout, limited by MaxOutput.
	Stdout io.Writer
	// Env adds environment variables ("NAME=value") to the runner's own. Only
	// names on the allow-list (see AllowedEnv) are accepted.
	Env []string
	// Inherit chooses which of the process's variables git sees.
	Inherit Inherit
	// SSHCommand is the program git uses for ssh:// and scp-style remotes,
	// without arguments. Default: GIT_SSH from the environment under Remote,
	// else "ssh". The repository's own core.sshCommand is always ignored.
	SSHCommand string
	// HooksPath is where hooks are read from. Default /dev/null: none run.
	HooksPath string
	// Untrusted says the repository's content is hostile (a runner cloning
	// a customer's repository): objects are checked on transfer.
	Untrusted bool
	// Timeout ends the run after this long (0: no total limit besides ctx).
	Timeout time.Duration
	// IdleTimeout ends the run when git neither writes nor reads for this
	// long (0: none). Use it instead of Timeout for large transfers.
	IdleTimeout time.Duration
	// Limits is a per-process memory, CPU and file size ceiling.
	Limits Limits
	// MaxOutput limits captured standard output (default DefaultMaxOutput).
	MaxOutput int64
	// Secrets are values removed from every message and from Result.Stderr.
	Secrets []string
	// AllowAlternates permits a repository that borrows objects from another.
	AllowAlternates bool
}

// Limits are ceilings applied to the git process before it runs. Zero means
// no limit. They need the launchway binary's help: see Reexec.
type Limits struct {
	// MemoryBytes caps the process's data segment and anonymous memory
	// (RLIMIT_DATA), which is what git allocates; mapped pack files do not
	// count against it.
	MemoryBytes uint64
	// CPUSeconds caps the CPU time (RLIMIT_CPU).
	CPUSeconds uint64
	// FileBytes caps the size of any one file git writes (RLIMIT_FSIZE), which
	// bounds how big a pack a clone can store.
	FileBytes uint64
}

func (l Limits) any() bool { return l.MemoryBytes > 0 || l.CPUSeconds > 0 || l.FileBytes > 0 }

// Result is what a run produced.
type Result struct {
	// Stdout is git's standard output when Spec.Stdout was nil. It is data,
	// not a message, so it is not redacted.
	Stdout string
	// Stderr is git's standard error, trimmed, with credentials removed.
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// ExitError is the error of a git command that ran and failed.
type ExitError struct {
	Code   int
	Stdout string // with credentials removed
	Stderr string // with credentials removed
}

// Error is what git said, trimmed; if it said nothing, the exit status.
func (e *ExitError) Error() string {
	if s := strings.TrimSpace(e.Stderr); s != "" {
		return s
	}
	if s := strings.TrimSpace(e.Stdout); s != "" {
		return s
	}
	return fmt.Sprintf("git exited with status %d", e.Code)
}

// Output is everything git printed, standard error first, trimmed.
func (e *ExitError) Output() string {
	return strings.TrimSpace(e.Stderr + e.Stdout)
}

// Runner starts git. The zero Runner finds git on PATH.
type Runner struct {
	// Binary is the git program; empty means "git" found on PATH.
	Binary string
}

// Default is the Runner every package uses.
var Default = &Runner{}

// Run starts git as Spec describes and waits for it.
func Run(ctx context.Context, s Spec) (Result, error) { return Default.Run(ctx, s) }

// Output runs git and returns its standard output.
func Output(ctx context.Context, s Spec) (string, error) {
	r, err := Default.Run(ctx, s)
	return r.Stdout, err
}

// Run starts git as Spec describes and waits for it. When git ran and failed
// the error is an *ExitError and Result still holds what it wrote.
func (r *Runner) Run(ctx context.Context, s Spec) (Result, error) {
	var res Result
	if len(s.Args) == 0 || strings.ContainsRune(strings.Join(s.Args, ""), 0) {
		return res, fmt.Errorf("%w: git needs arguments without NUL bytes", ErrDenied)
	}
	if err := s.checkRepo(); err != nil {
		return res, err
	}
	env, cfg, err := s.build()
	if err != nil {
		return res, err
	}
	prog, err := r.program()
	if err != nil {
		return res, err
	}
	argv := append(cfg, s.Args...)

	var cmd *exec.Cmd
	ctx2, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if s.Limits.any() {
		cmd, err = limitedCommand(ctx2, s.Limits, prog, argv)
		if err != nil {
			return res, err
		}
		env = append(env, reexecEnv+"=1")
	} else {
		cmd = exec.CommandContext(ctx2, prog, argv...) //nolint:gosec // the one place git is started: argument list, no shell, allow-listed environment
	}
	dir := s.Dir
	if dir == "" {
		dir = s.GitDir
	}
	cmd.Dir = dir
	cmd.Env = env
	setGroup(cmd)
	cmd.WaitDelay = killGrace

	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	touch := func() { last.Store(time.Now().UnixNano()) }
	if s.Stdin != nil {
		cmd.Stdin = &activityReader{r: s.Stdin, touch: touch}
	}
	var out *capture
	if s.Stdout != nil {
		cmd.Stdout = &activityWriter{w: s.Stdout, touch: touch}
	} else {
		limit := s.MaxOutput
		if limit <= 0 {
			limit = DefaultMaxOutput
		}
		out = &capture{max: limit, touch: touch, over: func() { cancel(ErrOutputLimit) }}
		cmd.Stdout = out
	}
	errBuf := &capture{max: MaxStderr, touch: touch, truncate: true}
	cmd.Stderr = errBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return res, fmt.Errorf("starting git: %w", err)
	}
	stop := watch(s, start, &last, cancel)
	werr := cmd.Wait()
	stop()
	res.Duration = time.Since(start)
	res.Stderr = s.clean(errBuf.String())
	if out != nil {
		res.Stdout = out.String()
	}
	if cause := context.Cause(ctx2); cause != nil && werr != nil {
		switch {
		case errors.Is(cause, ErrTimeout), errors.Is(cause, ErrIdle), errors.Is(cause, ErrOutputLimit):
			return res, fmt.Errorf("git %s: %w", s.Args[0], cause)
		case ctx.Err() != nil:
			return res, fmt.Errorf("git %s: %w", s.Args[0], ctx.Err())
		}
	}
	if werr != nil {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, &ExitError{Code: res.ExitCode, Stdout: s.clean(res.Stdout), Stderr: res.Stderr}
		}
		return res, fmt.Errorf("git %s: %s", s.Args[0], s.clean(werr.Error()))
	}
	return res, nil
}

// watch enforces the total and idle timeouts until the returned function is
// called.
func watch(s Spec, start time.Time, last *atomic.Int64, cancel context.CancelCauseFunc) (stop func()) {
	var timers []*time.Timer
	if s.Timeout > 0 {
		timers = append(timers, time.AfterFunc(s.Timeout, func() { cancel(ErrTimeout) }))
	}
	done := make(chan struct{})
	var once sync.Once
	if s.IdleTimeout > 0 {
		go func() {
			tick := s.IdleTimeout / 4
			if tick < 5*time.Millisecond {
				tick = 5 * time.Millisecond
			}
			t := time.NewTicker(tick)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					if time.Since(time.Unix(0, last.Load())) >= s.IdleTimeout {
						cancel(ErrIdle)
						return
					}
				}
			}
		}()
	}
	return func() {
		once.Do(func() { close(done) })
		for _, t := range timers {
			t.Stop()
		}
	}
}

func (r *Runner) program() (string, error) {
	if r.Binary != "" {
		return r.Binary, nil
	}
	p, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git is not installed or not on PATH: %w", err)
	}
	return p, nil
}

// clean removes credentials from a message.
func (s Spec) clean(msg string) string {
	msg = strings.TrimSpace(msg)
	for _, sec := range s.Secrets {
		if len(sec) >= 3 {
			msg = strings.ReplaceAll(msg, sec, "***")
		}
	}
	return redact.URLs(msg)
}

// checkRepo refuses a repository that borrows objects from elsewhere: an
// alternates file can point git at any directory the server can read.
func (s Spec) checkRepo() error {
	if s.AllowAlternates {
		return nil
	}
	var dirs []string
	if s.GitDir != "" {
		dirs = append(dirs, s.GitDir)
	}
	if s.Dir != "" {
		dirs = append(dirs, s.Dir, filepath.Join(s.Dir, ".git"))
	}
	for _, d := range dirs {
		for _, f := range []string{"objects/info/alternates", "objects/info/http-alternates"} {
			if _, err := os.Lstat(filepath.Join(d, f)); err == nil {
				return fmt.Errorf("%w: the repository at %s borrows objects from another (%s)", ErrDenied, d, f)
			}
		}
	}
	return nil
}

// capture is a bounded buffer that notes activity.
type capture struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	max      int64
	truncate bool // drop the excess silently instead of failing
	over     func()
	touch    func()
	tripped  bool
}

func (c *capture) Write(p []byte) (int, error) {
	c.touch()
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.max - int64(c.buf.Len())
	if int64(len(p)) <= room {
		c.buf.Write(p)
		return len(p), nil
	}
	if room > 0 {
		c.buf.Write(p[:room])
	}
	if !c.truncate && !c.tripped {
		c.tripped = true
		if c.over != nil {
			c.over()
		}
	}
	return len(p), nil // keep draining so git is not blocked before it is killed
}

func (c *capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

type activityWriter struct {
	w     io.Writer
	touch func()
}

func (a *activityWriter) Write(p []byte) (int, error) {
	a.touch()
	return a.w.Write(p)
}

type activityReader struct {
	r     io.Reader
	touch func()
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.touch()
	}
	return n, err
}
