package api

import (
	"context"

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
	return source.OutsideCredential{URL: ca.URL, Username: ca.Username, Secret: ca.Secret}, true, nil
}
