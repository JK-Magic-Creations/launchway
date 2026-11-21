package gitops

import (
	"context"
	"fmt"
	"strings"
)

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// existingWork looks for the work branch on the server, and returns its tip
// when it carries the trailer line: an earlier try already pushed this
// change. A work branch without the line is not ours to reuse: it is an
// error, not something to overwrite.
func (r *Repo) existingWork(ctx context.Context, line string) (string, error) {
	if r.WorkBranch == "" {
		return "", nil
	}
	out, err := r.git(ctx, nil, "ls-remote", "origin", "refs/heads/"+r.WorkBranch)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	ref := "refs/remotes/origin/" + r.WorkBranch
	if _, err := r.git(ctx, nil, "fetch", "--quiet", "--no-tags", "origin", "+refs/heads/"+r.WorkBranch+":"+ref); err != nil {
		return "", err
	}
	body, err := r.git(ctx, nil, "log", "-1", "--format=%B", ref, "--")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(body, "\n") {
		if strings.TrimRight(l, " \t\r") == line {
			return r.revParse(ctx, ref)
		}
	}
	return "", fmt.Errorf("the branch %s already exists and is not this deployment's change; it is left as it is: remove it, or ask an administrator to", r.WorkBranch)
}
