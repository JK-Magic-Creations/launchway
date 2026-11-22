package hosts

import (
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/azuredevops"
)

func init() {
	Register(Entry{
		Kind:  forge.KindAzureDevOps,
		Label: "Azure DevOps",
		Caps: func() (forge.Capabilities, []forge.Addition) {
			return azuredevops.Capabilities(), azuredevops.Additions()
		},
		New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
			p, err := azuredevops.New(c, g)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) {
			ep, err := azuredevops.EndpointsOf(c.BaseURL)
			if err != nil {
				return nil, err
			}
			return []string{ep.API.String()}, nil
		},
		// A service hook sends no delivery header: the provider names a delivery by the id in its payload.
		DeliveryHeader: "",
		Probe:          "that the token reads the projects and repositories of the allowed organizations (the host names no owner, scopes or expiry of a token) and one branch policy read; writes are not tried",
	})
}
