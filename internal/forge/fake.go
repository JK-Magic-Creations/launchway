package forge

// Fake is the skeleton of the fake forge server and provider that the
// conformance suite (story S34-04) fills in: a provider of any kind that
// answers from memory. Today it only declares capabilities and refuses every
// call, so that registrations and the matrix can be exercised.
type Fake struct {
	Base
}

// NewFake makes a fake provider of a kind with the capabilities given.
func NewFake(k Kind, caps Capabilities) *Fake {
	return &Fake{Base{K: k, Caps: caps}}
}

// FakeFactory is a Factory for the kind that makes fakes with those
// capabilities.
func FakeFactory(k Kind, caps Capabilities) Factory {
	return func(Connection) (Provider, error) { return NewFake(k, caps), nil }
}

// NoCapabilities declares every capability absent, with one workaround; a
// starting point for tests and for providers that grow capability by
// capability.
func NoCapabilities(reason, workaround string) Capabilities {
	c := Capabilities{}
	for _, cap := range AllCapabilities {
		c[cap] = Declaration{Support: None, Reason: reason, Workaround: workaround}
	}
	return c
}
