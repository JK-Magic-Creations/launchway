package storagespike

import "strings"

// splitSQL splits a schema into statements, CREATE INDEX statements
// included, dropping blank ones; used for the blob store's schema.
func splitSQL(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func isExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
}
