package api

// Git over SSH: the application side of internal/forge/gitssh. The SSH server
// settles who is calling (a registered public key, a deploy key, or a
// certificate from a trusted authority) and what is asked; everything after
// that is the code the HTTPS transport runs: gitDecide for the permission
// and the repository's state, the push policy (protected branches) inside
// the receive, gitPushed for the audit trail and the events.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/gitssh"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/refrules"
	"github.com/jk-magic-creations/launchway/internal/forge/sshkeys"
	"github.com/jk-magic-creations/launchway/internal/keyring"
)

const viaSSH = "ssh"

// SSHOptions are what StartSSH needs besides the server's own state.
type SSHOptions struct {
	Listen  string
	UserCAs []ssh.PublicKey
	// Limits; zero is the gitssh default.
	MaxConns, MaxPerAddr     int
	IdleTimeout, MaxDuration time.Duration
	// Ready receives the address once the server listens (for tests and logs).
	Ready func(net.Addr)
}

// SSHInfo is what the API says about Git over SSH.
type SSHInfo struct {
	Enabled bool `json:"enabled"`
	// Host and Port are what clone addresses name.
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	// Certificates says whether certificates from a trusted authority are accepted.
	Certificates bool `json:"certificates"`
}

// StartSSH starts the SSH server and returns when it is listening. It stops
// when ctx ends. Host keys are made by the first server to need them and
// shared through the database.
func (s *Server) StartSSH(ctx context.Context, o SSHOptions) error {
	if s.Forge == nil || s.Forge.Git == nil || s.Forge.Keys == nil {
		return errors.New("serving Git over SSH needs Git hosting")
	}
	seal, err := s.hostKeySealer(ctx)
	if err != nil {
		return err
	}
	signers, err := s.Forge.Keys.HostSigners(ctx, seal)
	if err != nil {
		return fmt.Errorf("the SSH host keys: %w", err)
	}
	srv, err := gitssh.New(gitssh.Config{HostKeys: signers, UserCAs: o.UserCAs, Handler: sshHandler{s}, Log: s.Log,
		MaxConns: o.MaxConns, MaxPerAddr: o.MaxPerAddr, IdleTimeout: o.IdleTimeout, MaxDuration: o.MaxDuration})
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.Listen)
	if err != nil {
		return fmt.Errorf("listening for SSH on %s: %w", o.Listen, err)
	}
	go func() {
		if err := srv.Serve(ctx, ln); err != nil {
			s.Log.Error("the SSH server stopped", "err", err)
		}
	}()
	if o.Ready != nil {
		o.Ready(ln.Addr())
	}
	return nil
}

// hostKeySealer seals the host keys under the installation's root key, each
// bound to its row, so a copy of the database alone gives no private key. With
// no root key it refuses to make new host keys and says how to create one.
func (s *Server) hostKeySealer(ctx context.Context) (sshkeys.Sealer, error) {
	svc, err := s.secretStore()
	if err != nil {
		return nil, err
	}
	if err := svc.RequireRootKey("Git over SSH"); err != nil {
		var n int
		if qerr := s.Engine.Database().QueryRow(ctx, `SELECT count(*) FROM ssh_host_keys`).Scan(&n); qerr != nil || n == 0 {
			return nil, err
		}
		s.Log.Warn("Git over SSH is using host keys that are stored in the clear, because this server has no root key; create one to seal them: " + keyring.HowToCreateARootKey)
	}
	return svc.HostKeys(), nil
}

// pushPolicy is the rule applied inside every push of this repository, from
// either transport: protected branches. nil when there are none.
func (s *Server) pushPolicy(c gitCaller, repo forge.Repository) func(context.Context, []githttp.RefUpdate) error {
	if c.pipe != nil {
		return s.pushPolicyPipeline(c, repo)
	}
	if s.Forge == nil || s.Forge.Rules == nil {
		return nil
	}
	return func(ctx context.Context, ups []githttp.RefUpdate) error {
		for _, u := range ups {
			if pulls.ReservedRef(u.Name) {
				return fmt.Errorf("%s is kept by the server (refs/pull/ for pull requests, refs/imported/ for history brought from another server), so nothing can be pushed there", u.Name)
			}
		}
		pats, err := s.Forge.Rules.Patterns(ctx, repo.ID)
		if err != nil {
			return errors.New("the repository's branch rules could not be read; nothing was changed")
		}
		if len(pats) == 0 {
			return nil
		}
		isAdmin := c.id != nil && s.authorize(c.id, auth.AdministerProject, repo.Path)
		us := make([]refrules.Update, len(ups))
		for i, u := range ups {
			us[i] = refrules.Update{Name: u.Name, Old: u.Old, New: u.New}
		}
		if err := refrules.Check(pats, us, isAdmin); err != nil {
			return err
		}
		return s.verifiedPush(ctx, c, repo, ups)
	}
}

// sshHandler is the application side of the SSH server.
type sshHandler struct{ s *Server }

func (h sshHandler) Authenticate(ctx context.Context, key ssh.PublicKey, remote string) (gitssh.Identity, error) {
	s := h.s
	k, err := s.Forge.Keys.ByFingerprint(ctx, ssh.FingerprintSHA256(key))
	if err != nil {
		return gitssh.Identity{}, errors.New("unknown key")
	}
	if k.Expired(s.Now()) {
		return gitssh.Identity{}, fmt.Errorf("the key %s has expired", k.Name)
	}
	c := gitCaller{sshKey: k.Fingerprint}
	switch k.Kind {
	case sshkeys.Deploy:
		kc := k
		c.deploy, c.user = &kc, "deploy key "+k.Name
	default:
		c.id = &identity{User: k.Owner, Groups: k.Groups, Via: viaSSH}
		c.user = k.Owner
	}
	return gitssh.Identity{Name: c.user, Fingerprint: k.Fingerprint, Data: struct {
		gitCaller
		keyID string
	}{c, k.ID}}, nil
}

func (h sshHandler) AuthenticatePrincipals(_ context.Context, principals []string, cert *ssh.Certificate, _ string) (gitssh.Identity, error) {
	if len(principals) == 0 || strings.TrimSpace(principals[0]) == "" {
		return gitssh.Identity{}, errors.New("the certificate names no principal")
	}
	c := gitCaller{id: &identity{User: principals[0], Via: viaSSH}, user: principals[0], sshKey: fmt.Sprintf("certificate %d", cert.Serial)}
	return gitssh.Identity{Name: c.user, Data: struct {
		gitCaller
		keyID string
	}{c, ""}}, nil
}

type sshCaller = struct {
	gitCaller
	keyID string
}

func (h sshHandler) Used(ctx context.Context, id gitssh.Identity, remote string) {
	if d, ok := id.Data.(sshCaller); ok && d.keyID != "" {
		if err := h.s.Forge.Keys.Touch(ctx, d.keyID, remote, h.s.Now()); err != nil {
			h.s.Log.Warn("recording that an SSH key was used", "err", err)
		}
	}
}

func (h sshHandler) Greeting(id gitssh.Identity) string {
	return fmt.Sprintf("Hi %s! You are signed in to Launchway; shell access is not provided.", id.Name)
}

func (h sshHandler) Serve(ctx context.Context, sess gitssh.Session, cmd gitssh.Command) int {
	s := h.s
	d, _ := sess.Identity.Data.(sshCaller)
	c := d.gitCaller
	say := func(format string, a ...any) { fmt.Fprintf(sess.Err, "Launchway: "+format+"\n", a...) }
	q := gitReq{svc: cmd.Service, class: repoRead, path: repoSubject(splitPath(cmd.Path))}
	if cmd.Service == githttp.ReceivePack {
		q.class = repoWrite
	}
	dec := s.gitDecide(ctx, q, c)
	switch {
	case dec.failed != nil:
		s.Log.Error("deciding a Git request over SSH", "err", dec.failed)
		say("something went wrong on the server; its log has the details")
		return 1
	case dec.code != 0:
		if dec.audit {
			s.gitDenied(sess.Remote, c, q.path, q.svc, dec.msg)
		}
		say("%s", dec.msg)
		return 1
	}
	res := s.Forge.Git.Stream(ctx, githttp.StreamRequest{
		Request:  githttp.Request{Repo: dec.repo, Service: cmd.Service, User: c.user, Notice: s.pushNotice(c, dec.repo, nil)},
		Protocol: cmd.Protocol, In: sess.In, Out: sess.Out, Policy: s.pushPolicy(c, dec.repo)})
	if cmd.Service == githttp.ReceivePack {
		s.gitPushed(ctx, sess.Remote, c, dec.repo, res)
	}
	var busy *githttp.ErrBusy
	switch {
	case errors.As(res.Err, &busy):
		say("%s", busy.Message)
		return 1
	case errors.Is(res.Err, githttp.ErrTooLarge):
		say("the push is larger than this server accepts; push less at a time or ask the operator to raise forge.max_push_mib")
		return 1
	case res.Err != nil && !errors.Is(res.Err, io.EOF):
		say("the transfer did not complete")
		return 1
	}
	return 0
}

func splitPath(p string) (owner, name string) {
	owner, name, _ = strings.Cut(p, "/")
	return owner, name
}
