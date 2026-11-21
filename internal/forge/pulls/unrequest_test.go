package pulls

import (
	"errors"
	"slices"
	"testing"
)

// A person asked to review can be taken off the list; one who was not asked
// is refused.
func TestRemoveReviewRequest(t *testing.T) {
	w, p := reviewWorld(t)
	if err := w.svc.RequestReview(w.ctx, repoID, p, "ada", []string{"grace", "linus"}); err != nil {
		t.Fatal(err)
	}
	p, _ = w.svc.Store.ByID(w.ctx, p.ID)
	if err := w.svc.RemoveReviewRequest(w.ctx, repoID, p, "ada", "grace"); err != nil {
		t.Fatal(err)
	}
	got, _ := w.svc.Store.ByID(w.ctx, p.ID)
	if !slices.Equal(got.Reviewers, []string{"linus"}) {
		t.Fatalf("reviewers after removal: %v", got.Reviewers)
	}
	if err := w.svc.RemoveReviewRequest(w.ctx, repoID, got, "ada", "grace"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing someone not asked: %v", err)
	}
}
