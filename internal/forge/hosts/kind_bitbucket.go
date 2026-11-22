package hosts

import (
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/bitbucket"
)

func init() {
	Register(Entry{
		Kind:          forge.KindBitbucket,
		Label:         "Bitbucket Cloud",
		NoContentType: true,
		Caps:          func() (forge.Capabilities, []forge.Addition) { return bitbucket.Capabilities(), bitbucket.Additions() },
		New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
			p, err := bitbucket.New(c, g)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) {
			ep, err := bitbucket.EndpointsOf(c.BaseURL)
			if err != nil {
				return nil, err
			}
			return []string{ep.API.String()}, nil
		},
		DeliveryHeader: bitbucket.DeliveryHeader,
		Probe:          "who the account is (if the token has one), the repositories of the allowed workspaces with the account's permission on them, and one branch restriction read",
	})
}
