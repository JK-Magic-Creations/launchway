package forge

import "sort"

// Kind names a backend.
type Kind string

// The known kinds. "plain" and "launchway" are Launchway's own.
const (
	KindPlain       Kind = "plain"
	KindLaunchway   Kind = "launchway"
	KindGitHub      Kind = "github"
	KindGitLab      Kind = "gitlab"
	KindBitbucket   Kind = "bitbucket"
	KindAzureDevOps Kind = "azuredevops"
	KindGitea       Kind = "gitea"
	KindForgejo     Kind = "forgejo"
	KindCodeCommit  Kind = "codecommit"
	// KindBitbucketDataCenter is the self-managed Bitbucket product, a separate API from Bitbucket Cloud (KindBitbucket).
	KindBitbucketDataCenter Kind = "bitbucket-datacenter"
)

var knownKinds = []Kind{KindPlain, KindLaunchway, KindGitHub, KindGitLab, KindBitbucket, KindAzureDevOps, KindGitea, KindForgejo, KindCodeCommit, KindBitbucketDataCenter}

// KnownKinds lists every kind the model names, whether or not a provider for
// it is registered.
func KnownKinds() []Kind {
	out := append([]Kind(nil), knownKinds...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	for _, v := range knownKinds {
		if v == k {
			return true
		}
	}
	return false
}

// Maturity is a label a provider carries until it passes the conformance
// suite against a live service. It is not a reason to stop.
type Maturity string

const (
	Experimental Maturity = "experimental"
	Supported    Maturity = "supported"
)
