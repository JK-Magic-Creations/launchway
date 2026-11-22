// Package securitycheck is the plain security checklist: what is and is not in place on one
// installation, in words a person can act on. The installer prints it when it finishes, `launchway
// doctor security` prints it on demand, and the server hands the same list to the first-run
// wizard's last page. It reads facts and decides nothing else; it changes nothing.
package securitycheck

// Facts are what the checklist is computed from. A fact the caller cannot know stays Unknown
// (the zero value of Tri), and the checklist says so instead of guessing.
type Facts struct {
	// SignIn: sign-in is required for everything.
	SignIn Tri
	// SignInKind is "built-in accounts", "identity provider" or "none".
	SignInKind string
	// Loopback: the server answers on this machine only.
	Loopback Tri
	// Encrypted: secrets in the database are sealed under a root key held outside it.
	Encrypted Tri
	// RootKeyCopied: someone took a recovery copy of the root key.
	RootKeyCopied Tri
	// TLS: the address people use is https (here or at a proxy).
	TLS Tri
	// NetworkPolicies: default-deny policies are installed AND the cluster enforces them.
	NetworkPolicies Tri
	// RunsSteps: steps run on a connected runner (a trial that runs them on the server host would
	// be listed as a warning).
	PublicExposure Tri
	// SecondFactor: every built-in administrator has a second factor.
	SecondFactor Tri
	// ProxyNamed: forwarding headers are believed only from the proxies named in server.trusted_proxies
	// (Yes), or from any peer because only the older trust_proxy is set (No); Unknown when no proxy is in use.
	ProxyNamed Tri
	// Kubernetes says the installation runs in a cluster (cluster-only items are skipped otherwise).
	Kubernetes bool
}

// Tri is a fact that may be unknown.
type Tri int

const (
	Unknown Tri = iota
	Yes
	No
)

// Of makes a known fact.
func Of(b bool) Tri {
	if b {
		return Yes
	}
	return No
}

// Item is one line of the checklist.
type Item struct {
	ID string `json:"id"`
	// State is "ok", "attention" (do this) or "unknown" (could not be checked from here).
	State string `json:"state"`
	Title string `json:"title"`
	// Detail says what was found and, for attention, what to do.
	Detail string `json:"detail"`
}

// Check computes the checklist.
func Check(f Facts) []Item {
	var out []Item
	add := func(id string, t Tri, title, ok, bad, unk string) {
		switch t {
		case Yes:
			out = append(out, Item{ID: id, State: "ok", Title: title, Detail: ok})
		case No:
			out = append(out, Item{ID: id, State: "attention", Title: title, Detail: bad})
		default:
			out = append(out, Item{ID: id, State: "unknown", Title: title, Detail: unk})
		}
	}
	kind := f.SignInKind
	if kind == "" {
		kind = "sign-in"
	}
	add("sign-in", f.SignIn, "Sign-in is required",
		"People must sign in ("+kind+") before they can see or do anything.",
		"Nobody has to sign in: anyone who can reach this server has full access. Keep it on this machine only, or turn on built-in accounts.",
		"Could not tell whether sign-in is required.")
	if f.SecondFactor != Unknown {
		add("second-factor", f.SecondFactor, "Administrators have a second factor",
			"Every built-in administrator signs in with a password and an authenticator.",
			"An administrator has no second factor. Ask them to set one up under their account.", "")
	}
	add("exposure", f.PublicExposure, "Not open to the internet",
		"Nothing publishes this installation to the internet. Put it behind your own proxy and TLS when others need it.",
		"This installation is reachable beyond your own network. Make sure that is intended and that TLS and sign-in are on.",
		"Could not tell who can reach this installation.")
	if f.Loopback != Yes {
		add("tls", f.TLS, "Traffic is encrypted in transit",
			"The address people use is https.",
			"The address is plain http. Passwords and sessions cross the network unprotected: use it only on a private network you trust, and add TLS (a certificate or a proxy) before anyone else uses it.",
			"Could not tell whether the address is https.")
	}
	add("proxy-addresses", f.ProxyNamed, "Client addresses come only from named proxies",
		"The client address and scheme are believed only from the proxies named in server.trusted_proxies.",
		"Client addresses are believed from any peer: server.trust_proxy is on without server.trusted_proxies, so anyone who reaches the server directly can forge theirs (and slip past a token's source-address limit). Set server.trusted_proxies to the address ranges of your proxies (Helm: server.trustedProxies).",
		"")
	add("encryption", f.Encrypted, "Stored secrets are encrypted",
		"Secrets in the database are sealed under a root key kept outside it, so a copy of the database alone reveals none.",
		"Secrets are stored without encryption at rest. Turn on encryption (the guide Encryption at rest) before storing real credentials.",
		"Could not read the encryption status from here (sign in as an administrator to see it).")
	add("root-key-copy", f.RootKeyCopied, "A recovery copy of the root key exists",
		"A copy of the root key was taken. Keep it somewhere safe and separate; without it the secrets cannot be read after a loss.",
		"No recovery copy of the root key has been taken. Take one in the setup guide, step Encryption, and keep it apart from the database backups.",
		"")
	if f.Kubernetes {
		add("network-policies", f.NetworkPolicies, "Network policies lock the pods down",
			"Default-deny policies are installed and this cluster enforces them.",
			"The policies are installed, but this cluster's network plugin does not enforce them, so the lockdown has no effect. Use a plugin that enforces NetworkPolicy (Cilium, Calico).",
			"Could not tell whether this cluster enforces network policies.")
	}
	// Unknowns with no text are dropped: not every installation can answer every question.
	kept := out[:0]
	for _, it := range out {
		if it.State == "unknown" && it.Detail == "" {
			continue
		}
		kept = append(kept, it)
	}
	return kept
}

// Attention counts the items that need action.
func Attention(items []Item) int {
	n := 0
	for _, i := range items {
		if i.State == "attention" {
			n++
		}
	}
	return n
}
