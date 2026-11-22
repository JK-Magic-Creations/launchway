package hosts

import (
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/bitbucketdc"
)

func init() {
	Register(Entry{
		Kind:  forge.KindBitbucketDataCenter,
		Label: "Bitbucket Data Center",
		Caps: func() (forge.Capabilities, []forge.Addition) {
			return bitbucketdc.Capabilities(), bitbucketdc.Additions()
		},
		New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
			p, err := bitbucketdc.New(c, g)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) {
			ep, err := bitbucketdc.EndpointsOf(c.BaseURL)
			if err != nil {
				return nil, err
			}
			return []string{ep.API.String()}, nil
		},
		DeliveryHeader: bitbucketdc.DeliveryHeader,
		Probe:          "that the token is accepted, the repositories of the allowed projects it holds read, write and admin permission on, whether auto-merge is on for one writable repository, and whether branch permissions can be read (the host names no token kind, scopes or expiry)",
	})
}
