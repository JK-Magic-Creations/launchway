package source

// Brief is a project's name and default branch: what a list that only groups and filters needs. Unlike List it reads nothing
// from the database, so it costs the same for five thousand projects as for five.
type Brief struct {
	Name          string
	DefaultBranch string
}

// Briefs lists every project in order.
func (m *Manager) Briefs() []Brief {
	cat := m.c()
	out := make([]Brief, 0, len(cat.order))
	for _, name := range cat.order {
		out = append(out, Brief{Name: name, DefaultBranch: cat.projects[name].DefaultBranch})
	}
	return out
}
