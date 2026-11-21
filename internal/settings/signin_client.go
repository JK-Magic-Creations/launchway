package settings

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/egress"
)

// GuardedHTTPClient is the client for calls to an identity provider that is being tested: it never
// follows a redirect and never connects to an internal address unless an administrator allowed the
// host (the same guard as CheckSignIn).
func GuardedHTTPClient() *http.Client { return egress.Default.HTTPClient(10 * time.Second) }

// DiscoverIssuer reads an identity provider's discovery document through the guarded client and says,
// in plain words, what is wrong with it: a problem text (empty when fine), and whether the address was
// refused because it is internal (an administrator may then allow the host).
func DiscoverIssuer(ctx context.Context, issuer string) (ep SignInEndpoints, problem string, blocked bool) {
	return discoverOIDC(ctx, strings.TrimRight(strings.TrimSpace(issuer), "/"), nil)
}
