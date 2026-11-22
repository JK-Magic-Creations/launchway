package engine

import "strings"

func validName(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func envNames(envs map[string]Environment) string {
	list := make([]Environment, 0, len(envs))
	for _, e := range envs {
		list = append(list, e)
	}
	sortEnvs(list)
	names := make([]string, len(list))
	for i, e := range list {
		names[i] = e.Name
	}
	return strings.Join(names, ", ")
}
