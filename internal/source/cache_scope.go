package source

// defaultBranchOf is the default branch of a project this manager has, or
// empty. The engine decides whose build cache entries are the default scope
// from it (see engine.SetDefaultBranch); for a project it cannot say the
// default branch of, nothing is written to the default scope.
func (m *Manager) defaultBranchOf(name string) string {
	if p := m.c().projects[name]; p != nil {
		return p.DefaultBranch
	}
	return ""
}
