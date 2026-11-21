package work

import "slices"

// Caller is who is acting and what they may reach. The store never decides
// permissions itself: the interface that received the request fills these in
// from the product's roles.
type Caller struct {
	Actor
	// Read says whether the caller may read a scope's work; Maintain whether
	// they maintain it (see confidential items, and who may move work to
	// Done where the workflow says so).
	Read     func(Scope) bool
	Maintain func(Scope) bool
	// Write says whether they may change work in the scope.
	Write func(Scope) bool
	// Agent is set when the caller is an agent: its limits apply.
	Agent *Agent
}

// System is the caller for what the server itself does (imports, the lease
// sweep, auto transitions): it reads and writes everywhere.
func System(via string) Caller {
	all := func(Scope) bool { return true }
	return Caller{Actor: Actor{Name: "launchway", Kind: KindSystem, Via: via}, Read: all, Maintain: all, Write: all}
}

func (c Caller) canRead(s Scope) bool      { return c.Read != nil && c.Read(s) }
func (c Caller) canWrite(s Scope) bool     { return c.Write != nil && c.Write(s) }
func (c Caller) isMaintainer(s Scope) bool { return c.Maintain != nil && c.Maintain(s) }

// canSee applies the whole of visibility: the scope, and for a confidential
// item, being a maintainer, the reporter or an assignee.
func (c Caller) canSee(it *Item) bool {
	if !c.canRead(it.Scope) {
		return false
	}
	if !it.Confidential || c.isMaintainer(it.Scope) || c.Kind == KindSystem {
		return true
	}
	if it.Reporter == c.Name {
		return true
	}
	return slices.ContainsFunc(it.Assignees, func(a Assignee) bool { return a.Name == c.Name })
}
