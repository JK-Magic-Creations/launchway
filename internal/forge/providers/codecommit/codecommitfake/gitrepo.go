package codecommitfake

import (
	"context"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: a test fake serving git's own http-backend; no request header becomes an environment variable of a proxy
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// seed makes the repository: main with a README, feature with one more file,
// the pull request 1 between them and an approval rule template.
func (h *Host) seed() {
	cmd := exec.CommandContext(context.Background(), "git", "init", "--bare", "-q", "-b", "main", filepath.Join(h.dir, Repo)) //nolint:gosec // G204: git on the fake's own temporary directory
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(string(out))
	}
	for _, kv := range [][2]string{{"http.receivepack", "true"}, {"http.uploadpack", "true"}, {"uploadpack.allowReachableSHA1InWant", "false"}} {
		if _, err := h.git(nil, "", "config", kv[0], kv[1]); err != nil {
			panic(err)
		}
	}
	if _, err := h.commitFile("main", "README.md", []byte("hello\n"), "first"); err != nil {
		panic(err)
	}
	if _, err := h.git(nil, "", "update-ref", "refs/heads/feature", h.Branch("main")); err != nil {
		panic(err)
	}
	if _, err := h.commitFile("feature", "feature.txt", []byte("feature\n"), "add a feature"); err != nil {
		panic(err)
	}
	h.addPull("feature", "main", "A feature")
	h.prs[0].approvals = []approval{{arn: "arn:aws:iam::" + Account + ":user/Mary_Major", state: "APPROVE"}}
	h.tmpl = []template{{name: "2-approver-rule-for-main", content: `{"Version":"2018-11-08","DestinationReferences":["refs/heads/main"],"Statements":[{"Type":"Approvers","NumberOfApprovalsNeeded":2,"ApprovalPoolMembers":["arn:aws:sts::123456789012:assumed-role/CodeCommitReview/*"]}]}`}}
}

// commitFile writes one file on a branch with git's plumbing (a temporary index),
// as a commit on the branch's tip, and moves the branch.
func (h *Host) commitFile(branch, path string, content []byte, msg string) (string, error) {
	idx, err := os.CreateTemp(h.dir, "index-")
	if err != nil {
		return "", err
	}
	idx.Close()
	os.Remove(idx.Name())
	defer os.Remove(idx.Name())
	env := []string{"GIT_INDEX_FILE=" + idx.Name()}
	parent := h.Branch(branch)
	if parent != "" {
		if _, err := h.git(env, "", "read-tree", parent); err != nil {
			return "", err
		}
	}
	blob, err := h.git(env, string(content), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	if _, err := h.git(env, "", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
		return "", err
	}
	tree, err := h.git(env, "", "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", msg}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	id, err := h.git(env, "", args...)
	if err != nil {
		return "", err
	}
	if _, err := h.git(env, "", "update-ref", "refs/heads/"+branch, id); err != nil {
		return "", err
	}
	return id, nil
}

// serveGit serves Git over HTTPS for /v1/repos/<name>: the request must carry
// a Basic user name and password that are a good signature of this repository's
// path, and git's http-backend answers it.
func (h *Host) serveGit(w http.ResponseWriter, r *http.Request) {
	name, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/repos/"), "/")
	user, pass, ok := r.BasicAuth()
	host := r.Host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	if name != Repo {
		h.violate(w, http.StatusNotFound, "RepositoryNotFound", "no repository %q", name)
		return
	}
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="CodeCommit"`)
		h.violate(w, http.StatusUnauthorized, "Unauthorized", "a Git request without credentials")
		return
	}
	if why, good := h.checkGit(user, pass, host, "/v1/repos/"+name); !good {
		h.violate(w, http.StatusForbidden, "Forbidden", "git credentials refused: %s", why)
		return
	}
	svc := r.URL.Query().Get("service")
	if strings.HasSuffix(r.URL.Path, "/git-receive-pack") || svc == "git-receive-pack" {
		h.record("GIT push")
		if h.Denied["GitPush"] {
			h.fail(w, http.StatusForbidden, "AccessDenied", "not authorized to perform: codecommit:GitPush")
			return
		}
	} else {
		h.record("GIT pull")
		if h.Denied["GitPull"] {
			h.fail(w, http.StatusForbidden, "AccessDenied", "not authorized to perform: codecommit:GitPull")
			return
		}
	}
	gp, _ := exec.CommandContext(r.Context(), "git", "--exec-path").Output() //nolint:gosec // G702: a fixed command line, no request value in it
	backend := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(gp)), "git-http-backend"), Root: "/v1/repos",
		Env: []string{"GIT_PROJECT_ROOT=" + h.dir, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + strings.SplitN(user, "%", 2)[0], "GIT_COMMITTER_NAME=Mary", "GIT_COMMITTER_EMAIL=m@example.com",
			"GIT_CONFIG_NOSYSTEM=1", "HOME=" + h.dir}}
	backend.ServeHTTP(w, r)
}
