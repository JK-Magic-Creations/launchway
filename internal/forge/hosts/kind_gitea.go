package hosts

import (
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/gitea"
)

// Gitea and Forgejo are one provider under two kinds.
func init() {
	for _, k := range []struct {
		kind  forge.Kind
		label string
	}{{forge.KindGitea, "Gitea"}, {forge.KindForgejo, "Forgejo"}} {
		kind := k.kind
		Register(Entry{
			Kind:  kind,
			Label: k.label,
			Caps:  func() (forge.Capabilities, []forge.Addition) { return gitea.Capabilities(), gitea.Additions() },
			New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
				p, err := gitea.New(c, g)
				if err != nil {
					return nil, err
				}
				return p, nil
			},
			Endpoints: func(c forge.Connection) ([]string, error) {
				ep, err := gitea.EndpointsOf(c.BaseURL)
				if err != nil {
					return nil, err
				}
				return []string{ep.API.String()}, nil
			},
			DeliveryHeader: gitea.DeliveryHeader(kind),
			Probe:          "who the account is, the repositories it owns or contributes to with their push permission, and one branch protection read",
		})
	}
}
