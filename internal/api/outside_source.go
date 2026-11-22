package api

import (
	"strings"

	"context"
	"github.com/jk-magic-creations/launchway/internal/forge"

	"github.com/jk-magic-creations/launchway/internal/source"
)

// Credential makes the server the source package's key to repositories kept
// on outside Git hosts (source.OutsideSource): the checkout credential of the
// Flight's connection, for the one git process that polls or fetches. The
// address was checked against the egress allow-list first.
func (s *Server) Credential(ctx context.Context, project string) (source.OutsideCredential, bool, error) {
	if s.Hosts == nil {
		return source.OutsideCredential{}, false, nil
	}
	ca, ok, err := s.Hosts.CheckedClone(ctx, project)
	if err != nil || !ok {
		return source.OutsideCredential{}, false, err
	}
	return source.OutsideCredential{URL: ca.URL, Username: ca.Username, Secret: ca.Secret, Resolve: ca.Resolve}, true, nil
}

// PullRequest is what the host says about one pull request of an attached
// Flight's repository (source.OutsideSource): its title, author, draft state,
// whether it is from a fork, and where the host shows it.
func (s *Server) PullRequest(ctx context.Context, project string, n int) (source.PullRequestInfo, bool, error) {
	if s.Hosts == nil {
		return source.PullRequestInfo{}, false, nil
	}
	pr, ok, err := s.Hosts.PullRequest(ctx, project, n)
	if err != nil || !ok {
		return source.PullRequestInfo{}, ok, err
	}
	fork, _ := pr.Ext["fork"].(bool)
	url, _ := pr.Ext["url"].(string)
	return source.PullRequestInfo{Number: n, Head: pr.Head, Base: strings.TrimPrefix(pr.Target.Name, "refs/heads/"), Title: pr.Title,
		Author: pr.Author.Login, Draft: pr.State == forge.ProposalDraft, Fork: fork, URL: url}, true, nil
}
