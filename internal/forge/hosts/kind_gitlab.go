package hosts

import (
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/gitlab"
)

func init() {
	Register(Entry{
		Kind:  forge.KindGitLab,
		Label: "GitLab",
		New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
			p, err := gitlab.New(c, g)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) {
			ep, err := gitlab.EndpointsOf(c.BaseURL)
			if err != nil {
				return nil, err
			}
			return []string{ep.API.String()}, nil
		},
		DeliveryHeader: gitlab.DeliveryHeader,
		Probe:          "who the token is, its scopes and expiry, the role it holds on each project it reaches, and one protected branch read",
	})
}
