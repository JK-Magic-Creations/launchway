package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Candidate is an image a retention rule would delete, and why.
type Candidate struct {
	Repo      string   `json:"repository"`
	Digest    string   `json:"digest"`
	Tags      []string `json:"tags"`
	Reason    string   `json:"reason"`
	Size      int64    `json:"size"`
	CreatedAt int64    `json:"created_at"`
	Attached  int      `json:"attached"` // signatures, SBOMs and scans that go with it
}

type imgRow struct {
	digest  string
	created int64
	size    int64
	tags    []string
	newest  int64 // when the newest of its tags was set
	media   string
}

// RetentionPreview says what RunRetention would delete: for one repository
// (scope "repository", name the repository), every repository of a project
// (scope "project", name it), or all ("" and ""). Nothing is changed.
func (s *Service) RetentionPreview(ctx context.Context, scope, name string) ([]Candidate, error) {
	return s.retention(ctx, scope, name, nil)
}

// RunRetention deletes what the retention rules say, and returns what it
// deleted. An image a deployment uses (Options.Protected), an image with a
// tag a rule keeps or an immutable tag is never deleted.
func (s *Service) RunRetention(ctx context.Context, scope, name string, who Who) ([]Candidate, error) {
	var done []Candidate
	cands, err := s.retention(ctx, scope, name, &who)
	if err != nil {
		return nil, err
	}
	for _, c := range cands {
		if _, err := s.DeleteManifest(ctx, c.Repo, c.Digest, who); err != nil {
			s.log.Warn("retention could not delete an image; it is kept", "repo", c.Repo, "digest", c.Digest, "err", err)
			continue
		}
		done = append(done, c)
	}
	if len(done) > 0 {
		s.emit(ctx, Event{Type: "retention", Actor: who.Name, Detail: fmt.Sprintf("deleted %d images by retention rules", len(done))})
	}
	return done, nil
}

func (s *Service) retention(ctx context.Context, scope, name string, _ *Who) ([]Candidate, error) {
	var repos []Repository
	var err error
	switch scope {
	case "":
		repos, err = s.ListRepositories(ctx, "", nil)
	case ScopeProject:
		repos, err = s.ListRepositories(ctx, name, nil)
	case ScopeRepository:
		var r Repository
		if r, err = s.repoRow(ctx, name); err == nil {
			repos = []Repository{r}
		}
	default:
		return nil, newErr(400, CodeNameInvalid, "retention runs for a repository, a project or everything", "")
	}
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range repos {
		cs, err := s.retentionOf(ctx, r.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

// retentionImages loads a repository's top-level images with their tags:
// manifests that are not children of an index and not attachments.
func (s *Service) retentionImages(ctx context.Context, repo string) (map[string]*imgRow, error) {
	imgs := map[string]*imgRow{}
	err := scanAll(ctx, s.db, `SELECT digest, created_at, size, media_type FROM registry_manifests m WHERE repo = ? AND subject = ''
		AND NOT EXISTS (SELECT 1 FROM registry_manifest_refs r WHERE r.repo = m.repo AND r.ref = m.digest AND r.kind = 'manifest')`, []any{repo},
		func(scan func(...any) error) error {
			var r imgRow
			if err := scan(&r.digest, &r.created, &r.size, &r.media); err != nil {
				return err
			}
			imgs[r.digest] = &r
			return nil
		})
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, s.db, `SELECT tag, digest, updated_at FROM registry_tags WHERE repo = ?`, []any{repo}, func(scan func(...any) error) error {
		var t, d string
		var at int64
		if err := scan(&t, &d, &at); err != nil {
			return err
		}
		if r := imgs[d]; r != nil && !isAttachmentTag(t) {
			r.tags = append(r.tags, t)
			if at > r.newest {
				r.newest = at
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Images that only carry attachment tags are attachments, not images.
	for d, r := range imgs {
		if len(r.tags) == 0 {
			var attTags int
			_ = s.db.QueryRow(ctx, `SELECT count(*) FROM registry_tags WHERE repo = ? AND digest = ?`, repo, d).Scan(&attTags)
			if attTags > 0 {
				delete(imgs, d)
			}
		}
	}

	return imgs, nil
}

func (s *Service) retentionOf(ctx context.Context, repo string) ([]Candidate, error) {
	eff, err := s.EffectivePolicy(ctx, repo)
	if err != nil {
		return nil, err
	}
	rule := eff.Retention
	if !eff.HasRetention || (rule.KeepLast == 0 && rule.UntaggedDays == 0) {
		return nil, nil
	}
	imgs, err := s.retentionImages(ctx, repo)
	if err != nil {
		return nil, err
	}
	var tagged []*imgRow
	var untagged []*imgRow
	for _, r := range imgs {
		sort.Strings(r.tags)
		if len(r.tags) > 0 {
			tagged = append(tagged, r)
		} else {
			untagged = append(untagged, r)
		}
	}
	sort.Slice(tagged, func(i, j int) bool {
		if tagged[i].newest != tagged[j].newest {
			return tagged[i].newest > tagged[j].newest
		}
		return tagged[i].digest < tagged[j].digest
	})
	sort.Slice(untagged, func(i, j int) bool { return untagged[i].digest < untagged[j].digest })

	kept := func(r *imgRow) bool {
		for _, t := range r.tags {
			if matchAny(rule.KeepTags, t) || matchAny(eff.ImmutableTags, t) {
				return true
			}
		}
		return s.o.Protected != nil && s.o.Protected(ctx, repo, r.digest)
	}
	del := map[string]string{}
	if rule.KeepLast > 0 {
		n := 0
		for _, r := range tagged {
			if kept(r) {
				continue
			}
			n++
			if n > rule.KeepLast {
				del[r.digest] = fmt.Sprintf("older than the newest %d tagged images", rule.KeepLast)
			}
		}
	}
	if rule.UntaggedDays > 0 {
		cutoff := s.o.Now().Add(-time.Duration(rule.UntaggedDays) * 24 * time.Hour).UnixMilli()
		for _, r := range untagged {
			if r.created < cutoff && !kept(r) {
				del[r.digest] = fmt.Sprintf("untagged and older than %d days", rule.UntaggedDays)
			}
		}
	}
	if err := s.cascadeChildren(ctx, repo, del); err != nil {
		return nil, err
	}
	var out []Candidate
	for d, why := range del {
		r := imgs[d]
		c := Candidate{Repo: repo, Digest: d, Reason: why}
		if r != nil {
			c.Tags, c.CreatedAt, c.Size = r.tags, r.created, r.size
		}
		if c.Tags == nil {
			c.Tags = []string{}
		}
		if m, err := s.manifestRow(ctx, repo, d); err == nil {
			c.Size, c.CreatedAt = m.TotalSize, m.CreatedAt
		}
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM registry_manifests WHERE repo = ? AND subject = ?`, repo, d).Scan(&c.Attached)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt || (out[i].CreatedAt == out[j].CreatedAt && out[i].Digest < out[j].Digest)
	})
	return out, nil
}

// cascadeChildren adds to del the children of an index that goes, which no
// other image and no tag keeps.
func (s *Service) cascadeChildren(ctx context.Context, repo string, del map[string]string) error {
	for changed := true; changed; {
		changed = false
		for d := range del {
			kids, err := column(ctx, s.db, `SELECT ref FROM registry_manifest_refs WHERE repo = ? AND manifest = ? AND kind = 'manifest'`, repo, d)
			if err != nil {
				return err
			}
			for _, c := range kids {
				if _, already := del[c]; already {
					continue
				}
				var tags, parents int
				_ = s.db.QueryRow(ctx, `SELECT count(*) FROM registry_tags WHERE repo = ? AND digest = ?`, repo, c).Scan(&tags)
				_ = s.db.QueryRow(ctx, `SELECT count(*) FROM registry_manifest_refs WHERE repo = ? AND ref = ? AND kind = 'manifest' AND manifest <> ?`, repo, c, d).Scan(&parents)
				stays := false
				if parents > 0 {
					parentsOf, err := column(ctx, s.db, `SELECT manifest FROM registry_manifest_refs WHERE repo = ? AND ref = ? AND kind = 'manifest' AND manifest <> ?`, repo, c, d)
					if err != nil {
						return err
					}
					for _, p := range parentsOf {
						if _, going := del[p]; !going {
							stays = true
						}
					}
				}
				if tags == 0 && !stays && (s.o.Protected == nil || !s.o.Protected(ctx, repo, c)) {
					del[c] = "part of " + short(d) + ", which is deleted"
					changed = true
				}
			}
		}
	}
	return nil
}

func short(d string) string {
	_, h, _ := strings.Cut(d, ":")
	if len(h) > 12 {
		h = h[:12]
	}
	return h
}
