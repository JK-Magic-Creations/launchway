package source

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OutsideCredential is what reading a repository kept on another Git host
// takes: the clone address and the connection's checkout credential.
type OutsideCredential struct {
	URL      string
	Username string
	Secret   string
}

// OutsideSource gives a Flight that is attached to a repository on an outside
// Git host the credential of its connection. The server implements it
// (internal/api/outside_source.go), and has checked, before it answers, that
// the address is one the connection's host allows (internal/forge/egress).
type OutsideSource interface {
	// Credential answers ok=false for a project that is not attached.
	Credential(ctx context.Context, project string) (c OutsideCredential, ok bool, err error)
}

type outsideHolder struct{ o OutsideSource }

// SetOutsideSource turns on reading repositories kept on outside Git hosts
// through their connections: private repositories poll, and the pipeline file
// is read from the mirror.
func (m *Manager) SetOutsideSource(o OutsideSource) { m.outsidePtr.Store(&outsideHolder{o}) }

func (m *Manager) outsideSource() OutsideSource {
	if h := m.outsidePtr.Load(); h != nil {
		return h.o
	}
	return nil
}

// fetchOutside brings the mirror up to date from the outside host when the
// project is attached to a repository there; done says it did (or failed to),
// so that the caller does not fetch with no credential. The credential reaches
// git as an HTTP header in the environment of the one git process: it is in no
// URL, in no configuration file of the mirror and in no log (git's own error
// text is redacted by gitRun).
func (p *project) fetchOutside(ctx context.Context) (done bool, err error) {
	if p.outside == nil {
		return false, nil
	}
	o := p.outside()
	if o == nil {
		return false, nil
	}
	cred, ok, err := o.Credential(ctx, p.Name)
	if err != nil {
		return true, fmt.Errorf("the connection for %s is not usable: %w", p.Name, err)
	}
	if !ok {
		return false, nil
	}
	env, err := outsideEnv(cred)
	if err != nil {
		return true, err
	}
	if _, statErr := os.Stat(filepath.Join(p.dir, "HEAD")); statErr != nil {
		_ = os.RemoveAll(p.dir)
		if err = os.MkdirAll(filepath.Dir(p.dir), 0o750); err == nil {
			_, err = gitRun(ctx, "", env, "", "clone", "--mirror", "--quiet", "--", cred.URL, p.dir)
			if err != nil {
				_ = os.RemoveAll(p.dir) // a cut-off clone must be made again, not fetched into
			}
		}
	} else {
		if cur, _ := gitOut(ctx, p.dir, "remote", "get-url", "origin"); strings.TrimSpace(cur) != cred.URL {
			_, err = gitOut(ctx, p.dir, "remote", "set-url", "origin", cred.URL)
		}
		if err == nil {
			_, err = gitRun(ctx, p.dir, env, "", "remote", "update", "--prune")
		}
	}
	if err != nil {
		err = fmt.Errorf("fetching from the outside host: %w", err)
	}
	return true, err
}

// outsideEnv is the environment of the one git process that talks to the
// outside host: https and http only, no redirects (a redirect could carry the
// header to another host), and the credential as an Authorization header sent
// to the origin of the clone address only.
func outsideEnv(c OutsideCredential) ([]string, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return nil, fmt.Errorf("the connection's clone address is not an http(s) address without a user name")
	}
	origin := u.Scheme + "://" + u.Host
	n, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	env := []string{"GIT_ALLOW_PROTOCOL=http:https"}
	// The server's own Git settings are inherited by gitexec, which takes our
	// count as final: copy theirs, then add ours after them.
	for i := 0; i < n; i++ {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, os.Getenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i))),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, os.Getenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i))))
	}
	basic := base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Secret))
	env = append(env,
		"GIT_CONFIG_KEY_"+strconv.Itoa(n)+"=http.followRedirects", "GIT_CONFIG_VALUE_"+strconv.Itoa(n)+"=false",
		"GIT_CONFIG_KEY_"+strconv.Itoa(n+1)+"=http."+origin+"/.extraHeader", "GIT_CONFIG_VALUE_"+strconv.Itoa(n+1)+"=Authorization: Basic "+basic,
		"GIT_CONFIG_COUNT="+strconv.Itoa(n+2))
	return env, nil
}
