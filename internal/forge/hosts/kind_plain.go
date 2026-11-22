package hosts

import (
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/anygit"
)

func init() {
	Register(Entry{
		Kind:  forge.KindPlain,
		Label: "Any Git server",
		Caps:  func() (forge.Capabilities, []forge.Addition) { return anygit.Capabilities(), anygit.Additions() },
		New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
			p, err := anygit.New(c, g)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) {
			u, err := anygit.EndpointsOf(c.BaseURL)
			if err != nil {
				return nil, err
			}
			return []string{u.String()}, nil
		},
		// A plain Git server sends no webhook, so there is no delivery to name.
		DeliveryHeader: "",
		Probe:          "that the credential is readable and its form is right (a token, or a user name and password); a Git server lists no repositories and names no account, so the credential is proved against a repository when you enter its path, and reads and writes are not tried",
	})
}
