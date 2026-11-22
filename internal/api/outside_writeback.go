package api

// GitOps write-back to a repository kept on an outside Git host, through a
// connection (write_back.repo is connection:owner/name). The deploy step
// pushes with the host's credential, which is handed to it only when the
// step is leased, never stored with the run and never printed. To open and
// follow the pull request it shows a handle: a claim signed by this server
// that names the connection, repository, branches and mode, and expires, so
// the runner cannot ask for anything else.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
)

type outsideClaim struct {
	Connection string `json:"c"`
	Repo       string `json:"r"`
	Branch     string `json:"b"`
	Work       string `json:"w"`
	Mode       string `json:"m"`
	Project    string `json:"p"`
	Deployment string `json:"d"`
	Expires    int64  `json:"e"`
}

const outsideHandlePrefix = "outside."

func (s *Server) signOutside(ctx context.Context, c outsideClaim) (string, error) {
	key, err := s.hostedKey(ctx)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(c)
	body := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("outside-write-back." + body))
	return outsideHandlePrefix + body + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

func (s *Server) verifyOutside(ctx context.Context, handle string) (outsideClaim, error) {
	var c outsideClaim
	rest, ok := strings.CutPrefix(handle, outsideHandlePrefix)
	body, sig, ok2 := strings.Cut(rest, ".")
	if !ok || !ok2 {
		return c, errors.New("not a write-back handle")
	}
	key, err := s.hostedKey(ctx)
	if err != nil {
		return c, err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("outside-write-back." + body))
	want, _ := base64.RawURLEncoding.DecodeString(sig)
	raw, err2 := base64.RawURLEncoding.DecodeString(body)
	if !hmac.Equal(want, m.Sum(nil)) || err2 != nil || json.Unmarshal(raw, &c) != nil {
		return c, errors.New("the write-back handle is not valid")
	}
	if s.Now().UnixMilli() > c.Expires {
		return c, errors.New("the write-back handle has expired; run the deploy step again")
	}
	return c, nil
}

// mintOutsideWriteBack gives a leased deploy job the credential and handle
// for a repository on an outside host, or says in wb.Error why it cannot.
func (s *Server) mintOutsideWriteBack(ctx context.Context, job *engine.Job) {
	wb, d := job.Deploy.WriteBack, job.Deploy
	conn, path, _ := hosts.SplitRepo(wb.Repo)
	if s.Hosts == nil {
		wb.Error = "this Airfield writes back to a repository on an outside host, but this server has no connections to outside hosts"
		return
	}
	push, err := s.Hosts.WriteBackAuth(ctx, conn, path, d.Branch, wb.Mode, wb.Merge)
	if err != nil {
		wb.Error = errText(err)
		return
	}
	until := 2 * time.Hour
	if d.Deadline > 0 {
		until = max(time.UnixMilli(d.Deadline).Sub(s.Now())+time.Hour, 2*time.Hour)
	}
	h, err := s.signOutside(ctx, outsideClaim{Connection: conn, Repo: path, Branch: d.Branch, Work: wb.WorkBranch, Mode: wb.Mode,
		Project: job.Project, Deployment: d.Deployment, Expires: s.Now().Add(until).UnixMilli()})
	if err != nil {
		wb.Error = "a handle for the write-back could not be made: " + err.Error()
		return
	}
	wb.Outside, wb.Token, wb.Username, wb.Handle, wb.RepoID = true, push.Secret, push.Username, h, "outside:"+conn+":"+path
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "run:" + job.RunID, Action: "outside_host.write_back", Project: job.Project,
		Target: wb.Repo, Outcome: "ok", Detail: "credential for " + wb.Mode + " write-back to " + d.Branch + " handed to the deploy step"})
}

// outsideDeployPull serves the deploy step's open and status calls for an outside repository.
func (s *Server) outsideDeployPull(w http.ResponseWriter, r *http.Request, handle, op string, number int, title, body string) {
	c, err := s.verifyOutside(r.Context(), handle)
	if err != nil || s.Hosts == nil || c.Mode != "pull_request" {
		writeErr(w, http.StatusForbidden, "this write-back handle is not valid for a pull request (it is expired, or the Airfield does not write back by pull request)")
		return
	}
	var out hosts.DeployPull
	switch op {
	case "open":
		out, err = s.Hosts.OpenDeployPull(r.Context(), c.Connection, c.Repo, c.Work, c.Branch, title, body)
		if err == nil {
			s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "pipeline:" + c.Project, Action: "outside_host.pull_request", Project: c.Project,
				Target: c.Connection + ":" + c.Repo, Outcome: "ok", Detail: "pull request " + out.URL + " from " + c.Work + " into " + c.Branch})
		}
	case "status":
		out, err = s.Hosts.DeployPullStatus(r.Context(), c.Connection, c.Repo, number)
	default:
		writeErr(w, http.StatusBadRequest, "op is open or status")
		return
	}
	if err != nil {
		s.failHosts(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deployPullOut{Number: out.Number, URL: out.URL, State: out.State, Commit: out.Commit, WaitingFor: out.WaitingFor})
}
