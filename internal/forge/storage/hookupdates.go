package storage

import "strings"

// realRefUpdates keeps, from the lines git gives the reference-transaction
// hook, the updates of real refs, once each.
//
// Git older than about 2.50 adds a line for HEAD to the transaction whenever the
// branch HEAD points at is updated (it does so to write HEAD's reflog), and
// reports it to the hook like any other update. HEAD is not a ref the server
// manages: it is the symbolic name of refs/heads/main in the cache, and
// the branch's own line already says everything. Taken as a ref it was recorded
// in the ledger, and the cache then failed to follow the ledger ("multiple
// updates for 'refs/heads/main' (including one via symref 'HEAD')"). Newer git
// does not send the line, so dropping it makes every git behave the same.
func realRefUpdates(ups []RefUpdate) []RefUpdate {
	out := make([]RefUpdate, 0, len(ups))
	seen := map[string]bool{}
	for _, u := range ups {
		if !strings.HasPrefix(u.Name, "refs/") || seen[u.Name] {
			continue
		}
		seen[u.Name] = true
		out = append(out, u)
	}
	return out
}
