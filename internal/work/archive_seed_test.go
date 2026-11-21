package work_test

import (
	"context"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// seedArchive writes rows as the archive import would.
func seedArchive(t *testing.T, s *work.Store) {
	t.Helper()
	db := s.DB()
	ctx := context.Background()
	exec := func(q string, a ...any) {
		if _, err := db.Exec(ctx, q, a...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO imported_issues(repo_id, number, title, state, author, body, labels, assignees, is_pull, created_at, updated_at, closed_at) VALUES('repo1',7,'Old bug','closed','zoe','body text','["bug"]','["bob"]',0,1704164645000,1704164645000,1704250000000)`)
	exec(`INSERT INTO imported_issues(repo_id, number, title, state, author, body, labels, assignees, is_pull, created_at, updated_at, closed_at) VALUES('repo1',11,'Open one','open','yan','','[]','[]',0,1704164645000,1704164645000,0)`)
	exec(`INSERT INTO imported_issues(repo_id, number, title, state, author, body, labels, assignees, is_pull, created_at, updated_at, closed_at) VALUES('repo1',9,'Fix old bug','closed','zoe','Fixes #7','[]','[]',1,1704164645000,1704164645000,0)`)
	exec(`INSERT INTO imported_comments(repo_id, issue_number, position, source_id, author, body, created_at, updated_at) VALUES('repo1',7,0,'1','bob','first',1704240000000,0)`)
	exec(`INSERT INTO imported_comments(repo_id, issue_number, position, source_id, author, body, created_at, updated_at) VALUES('repo1',7,1,'2','zoe','second',1704250000000,0)`)
}
