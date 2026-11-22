package hosts

import (
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/github"
)

func init() {
	Register(Entry{
		Kind:  forge.KindGitHub,
		Label: "GitHub",
		Caps:  func() (forge.Capabilities, []forge.Addition) { return github.Capabilities(), github.Additions() },
		New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
			p, err := github.New(c, g)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) {
			ep, err := github.EndpointsOf(c.BaseURL, c.UploadURL)
			if err != nil {
				return nil, err
			}
			return []string{ep.API.String(), ep.Upload.String()}, nil
		},
		DeliveryHeader: "X-GitHub-Delivery",
		Probe:          "who the token is, its expiry, the repositories it reaches with their push permission, and one branch protection read",
	})
}
