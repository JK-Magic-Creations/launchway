package hosts

import (
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/codecommit"
)

func init() {
	Register(Entry{
		Kind:  forge.KindCodeCommit,
		Label: "AWS CodeCommit",
		Caps: func() (forge.Capabilities, []forge.Addition) {
			return codecommit.Capabilities(), codecommit.Additions()
		},
		New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
			p, err := codecommit.New(c, g)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) {
			ep, err := codecommit.EndpointsOf(c.BaseURL)
			if err != nil {
				return nil, err
			}
			// The API, the token service (for a role) and the Git endpoint: each is egress-checked.
			return []string{ep.API.String(), ep.STS.String(), ep.GitURL()}, nil
		},
		// CodeCommit sends no webhook, so there is no delivery to name.
		DeliveryHeader: "",
		Probe:          "that the credentials (an access key, or a role they assume) list the repositories of the region, read one repository's branches, pull requests and approval rule templates; writes are not tried",
	})
}
