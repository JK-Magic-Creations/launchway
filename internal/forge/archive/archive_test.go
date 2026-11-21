package archive_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/archive"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

type txDB struct{ *engine.Database }

func (d txDB) BeginTx(ctx context.Context) (archive.Tx, error) { return d.Begin(ctx) }

func newStore(t *testing.T) *archive.Store {
	t.Helper()
	e, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return archive.New(txDB{e.Database()})
}

func doc(n int) archive.Document {
	d := archive.Document{Format: archive.Format, Source: archive.Source{Name: "old", URL: "https://old.example/acme/shop"}}
	for i := 1; i <= n; i++ {
		state := "open"
		if i%3 == 0 {
			state = "closed"
		}
		is := archive.Issue{Number: i, Title: fmt.Sprintf("Issue %d about widgets", i), State: state, Author: "ada", Body: "The body of " + fmt.Sprint(i),
			CreatedAt: "2024-01-02T03:04:05Z", Labels: []string{"bug"}}
		if i == 7 {
			is.Comments = []archive.Comment{{ID: "9", Author: "bob", Body: "Try the 100%_safe Zebra option", CreatedAt: "2024-01-03T00:00:00Z"}, {Author: "ada", Body: "second"}}
		}
		d.Issues = append(d.Issues, is)
	}
	return d
}

func TestImportListSearchAndReplace(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	res, err := s.Import(ctx, "repo1", "ada", doc(120))
	if err != nil || res.Issues != 120 || res.Comments != 2 || res.Replaced != 0 || res.Total != 120 {
		t.Fatalf("%+v %v", res, err)
	}
	// Another repository's issues are not mixed in.
	if _, err := s.Import(ctx, "repo2", "ada", doc(3)); err != nil {
		t.Fatal(err)
	}
	pg, err := s.List(ctx, "repo1", archive.Query{})
	if err != nil || pg.Total != 120 || len(pg.Items) != 50 || pg.Items[0].Number != 120 || pg.Open != 80 || pg.Closed != 40 || pg.Items[0].Body != "" {
		t.Fatalf("%+v %v", pg, err)
	}
	pg, _ = s.List(ctx, "repo1", archive.Query{Page: 3})
	if len(pg.Items) != 20 || pg.Items[19].Number != 1 {
		t.Fatalf("last page: %d items", len(pg.Items))
	}
	if pg, _ = s.List(ctx, "repo1", archive.Query{State: "closed", PerPage: 10}); pg.Total != 40 || len(pg.Items) != 10 || pg.Items[0].State != "closed" {
		t.Fatalf("closed: %+v", pg)
	}
	// Search finds a word that is only in a comment, ignoring case, and treats % and _ literally.
	if pg, _ = s.List(ctx, "repo1", archive.Query{Text: "ZEBRA"}); pg.Total != 1 || pg.Items[0].Number != 7 {
		t.Fatalf("search: %+v", pg)
	}
	if pg, _ = s.List(ctx, "repo1", archive.Query{Text: "100%_safe"}); pg.Total != 1 {
		t.Fatalf("literal wildcard characters: %+v", pg)
	}
	if pg, _ = s.List(ctx, "repo1", archive.Query{Text: "%"}); pg.Total != 1 {
		t.Fatalf("a bare %%: %+v", pg)
	}
	if pg, _ = s.List(ctx, "repo1", archive.Query{Text: "widgets 1"}); pg.Total != 0 {
		t.Fatalf("phrase: %+v", pg)
	}
	is, err := s.Get(ctx, "repo1", 7)
	if err != nil || len(is.Comments) != 2 || is.Comments[0].Author != "bob" || is.Comments[1].Body != "second" || is.Body != "The body of 7" || is.CommentCount != 2 {
		t.Fatalf("%+v %v", is, err)
	}
	if _, err := s.Get(ctx, "repo1", 999); err == nil {
		t.Fatal("found an issue that was never imported")
	}
	if _, err := s.Get(ctx, "repo2", 7); err == nil {
		t.Fatal("read another repository's issue")
	}

	// The same export again replaces, and a corrected issue replaces its comments.
	again := doc(120)
	again.Issues[6].Comments = []archive.Comment{{Author: "cy", Body: "only one now"}}
	again.Issues[6].Title = "Corrected"
	res, err = s.Import(ctx, "repo1", "ada", again)
	if err != nil || res.Replaced != 120 || res.Total != 120 {
		t.Fatalf("%+v %v", res, err)
	}
	if is, _ = s.Get(ctx, "repo1", 7); is.Title != "Corrected" || len(is.Comments) != 1 {
		t.Fatalf("%+v", is)
	}
}

func TestImportRefusesBadDocumentsWholly(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	for name, mod := range map[string]func(*archive.Document){
		"another format": func(d *archive.Document) { d.Format = "other/2" },
		"no issues":      func(d *archive.Document) { d.Issues = nil },
		"no number":      func(d *archive.Document) { d.Issues[1].Number = 0 },
		"twice":          func(d *archive.Document) { d.Issues[1].Number = 1 },
		"state":          func(d *archive.Document) { d.Issues[1].State = "merged" },
		"time":           func(d *archive.Document) { d.Issues[1].CreatedAt = "yesterday" },
		"too long":       func(d *archive.Document) { d.Issues[1].Body = strings.Repeat("x", archive.MaxBodyBytes+1) },
	} {
		d := doc(3)
		mod(&d)
		if _, err := s.Import(ctx, "r", "ada", d); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if pg, _ := s.List(ctx, "r", archive.Query{}); pg.Total != 0 {
		t.Fatalf("a refused document left %d issues", pg.Total)
	}
	big := doc(archive.MaxIssuesPerImport + 1)
	if _, err := s.Import(ctx, "r", "ada", big); err == nil {
		t.Error("accepted more issues than one import may hold")
	}
}
