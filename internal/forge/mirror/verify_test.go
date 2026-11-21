package mirror_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

func TestVerifyProvesEveryRefEqualAndNamesWhatIsNot(t *testing.T) {
	o := newOldServer(t, oldToken)
	web := o.add(t, "acme/web", fillTwo)
	o.add(t, "acme/svc", fillOne)
	w, ctx := importWorld(t, o), context.Background()
	src := mirror.SourceRequest{Connection: "old"}
	if res, err := w.h.Mirrors.Verify(ctx, src); err != nil || res.Equal || res.Summary["missing"] != 2 {
		t.Fatalf("before any import: %v %+v", err, res)
	}
	if _, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: src, Wait: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	res, err := w.h.Mirrors.Verify(ctx, src)
	if err != nil || !res.Equal || res.Summary["equal"] != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	// The source moves a branch, adds a tag and deletes one: verify names each.
	web.commit("after the import")
	web.git("push", "-q", "origin", "main")
	web.git("tag", "late")
	web.git("push", "-q", "origin", "late")
	web.git("push", "-q", "origin", ":refs/heads/dev")
	res, _ = w.h.Mirrors.Verify(ctx, src)
	var web2 mirror.VerifyItem
	for _, it := range res.Items {
		if it.Repository == "acme/web" {
			web2 = it
		}
	}
	joined := strings.Join(web2.Differences, "\n")
	if res.Equal || web2.Status != mirror.VerifyDifferent || !strings.Contains(joined, "refs/heads/main: source") ||
		!strings.Contains(joined, "refs/tags/late: missing here") || !strings.Contains(joined, "refs/heads/dev: not in the source") {
		t.Fatalf("%+v", res)
	}
	// A sync brings it level again, and verify says so.
	mirrors := mustAll(t, w)
	for _, m := range mirrors {
		w.mustSync(m.ID)
	}
	if res, _ = w.h.Mirrors.Verify(ctx, src); !res.Equal {
		t.Fatalf("%+v", res)
	}
}
