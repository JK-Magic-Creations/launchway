package refrules

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// Who may do what to a protected tag, by role.
const (
	// RoleNobody is no role: nobody may.
	RoleNobody = ""
	// RoleOperator is anyone who may push to the repository.
	RoleOperator = "operator"
	// RoleAdmin is an administrator of the repository.
	RoleAdmin = "admin"
)

// TagRule protects the tags whose names match Pattern (v*, release-*). By
// default only an administrator may make such a tag and nobody may move or
// delete one: a version tag that has been seen is a promise.
type TagRule struct {
	// Pattern names the tags the rule covers: v*, 2026-*, latest.
	Pattern string `json:"pattern"`
	// CreateRole is the lowest role that may make a matching tag: admin (the
	// default) or operator.
	CreateRole string `json:"create_role"`
	// ChangeRole is the lowest role that may move or delete a matching tag:
	// empty for nobody (the default) or admin.
	ChangeRole string `json:"change_role"`
	// Pipelines are Flight names or patterns whose own credential may make a
	// matching tag, whatever CreateRole says; it never lets one move or delete.
	Pipelines []string `json:"pipelines"`
}

const maxTagRules = 100

// ValidateTagRules checks rules before they are stored and fills in the defaults.
func ValidateTagRules(rs []TagRule) error {
	if len(rs) > maxTagRules {
		return fmt.Errorf("at most %d protected tag patterns", maxTagRules)
	}
	seen := map[string]bool{}
	for i := range rs {
		r := &rs[i]
		r.Pattern = strings.TrimSpace(r.Pattern)
		p := r.Pattern
		if p == "" || len(p) > 200 || strings.ContainsAny(p, " \\[]?\x00") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "refs/") {
			return fmt.Errorf("%q is not a tag name or pattern: use names such as v* or release-*", p)
		}
		if _, err := path.Match(p, "x"); err != nil {
			return fmt.Errorf("%q is not a valid pattern", p)
		}
		if seen[p] {
			return fmt.Errorf("the pattern %s is given twice", p)
		}
		seen[p] = true
		if r.CreateRole == "" {
			r.CreateRole = RoleAdmin
		}
		if r.CreateRole != RoleAdmin && r.CreateRole != RoleOperator {
			return fmt.Errorf("%s: tags are made by the admin or operator role", p)
		}
		if r.ChangeRole != RoleNobody && r.ChangeRole != RoleAdmin {
			return fmt.Errorf("%s: tags are moved or deleted by nobody (leave it empty) or by the admin role", p)
		}
		if len(r.Pipelines) > 100 {
			return fmt.Errorf("%s: at most 100 pipelines", p)
		}
		clean := []string{}
		for _, f := range r.Pipelines {
			f = strings.TrimSpace(f)
			if _, err := path.Match(f, ""); err != nil || f == "" || f == "*" {
				return fmt.Errorf("%s: %q is not a Flight name or pattern (* alone would admit every Flight)", p, f)
			}
			clean = append(clean, f)
		}
		r.Pipelines = clean
	}
	return nil
}

// TagRules returns a repository's protected tag rules, in the order they were made.
func (s *Store) TagRules(ctx context.Context, repoID string) ([]TagRule, error) {
	rows, err := store.TagRulesOf(ctx, s.db, repoID)
	if err != nil {
		return nil, err
	}
	out := []TagRule{}
	for _, r := range rows {
		t := TagRule{Pattern: r.Pattern, CreateRole: r.CreateRole, ChangeRole: r.ChangeRole}
		if json.Unmarshal([]byte(r.Pipelines), &t.Pipelines) != nil || t.Pipelines == nil {
			t.Pipelines = []string{}
		}
		out = append(out, t)
	}
	return out, nil
}

// SetTagRules replaces a repository's protected tag rules.
func (s *Store) SetTagRules(ctx context.Context, repoID string, rs []TagRule, by string) error {
	if err := ValidateTagRules(rs); err != nil {
		return err
	}
	if err := store.ReplaceTagRules(ctx, s.db, repoID); err != nil {
		return err
	}
	now := s.now().UnixMilli()
	for i, r := range rs {
		pl, _ := json.Marshal(r.Pipelines)
		row := store.TagRuleRow{Pattern: r.Pattern, CreateRole: r.CreateRole, ChangeRole: r.ChangeRole, Pipelines: string(pl)}
		if err := store.AddTagRule(ctx, s.db, ids.New("tgr"), repoID, row, now+int64(i), by); err != nil {
			return err
		}
	}
	return nil
}

// TagActor is who pushes: an administrator of the repository, someone else who
// may push, or the credential of a Flight's pipeline (Pipeline is its name).
type TagActor struct {
	Admin    bool
	Pipeline string
}

// CheckTags judges the tag updates of a push: nil if they may go on,
// otherwise the sentence the pusher is told. An update is a creation (the old
// id is empty or zeros), a deletion (the new id is zeros) or a move.
func CheckTags(rules []TagRule, updates []Update, who TagActor) error {
	for _, u := range updates {
		name, ok := strings.CutPrefix(u.Name, "refs/tags/")
		if !ok {
			continue
		}
		var rule *TagRule
		for i := range rules {
			if m, _ := path.Match(rules[i].Pattern, name); m {
				rule = &rules[i]
				break
			}
		}
		if rule == nil {
			continue
		}
		switch {
		case strings.Trim(u.Old, "0") == "": // creation
			if who.Pipeline != "" {
				if matchAny(rule.Pipelines, who.Pipeline) {
					continue
				}
				return fmt.Errorf("the tag %s is protected (pattern %s): the pipeline of %s is not named by the rule; an administrator can add it under the repository's protected tags", name, rule.Pattern, who.Pipeline)
			}
			if rule.CreateRole == RoleAdmin && !who.Admin {
				return fmt.Errorf("the tag %s is protected (pattern %s): only an administrator of the repository may make a tag like it", name, rule.Pattern)
			}
		case strings.Trim(u.New, "0") == "": // deletion
			if who.Pipeline != "" || rule.ChangeRole != RoleAdmin || !who.Admin {
				return fmt.Errorf("the tag %s is protected (pattern %s) and cannot be deleted%s", name, rule.Pattern, changeHint(rule))
			}
		default: // a move
			if who.Pipeline != "" || rule.ChangeRole != RoleAdmin || !who.Admin {
				return fmt.Errorf("the tag %s is protected (pattern %s) and cannot be moved to another commit%s", name, rule.Pattern, changeHint(rule))
			}
		}
	}
	return nil
}

func changeHint(r *TagRule) string {
	if r.ChangeRole == RoleAdmin {
		return ": only an administrator of the repository may"
	}
	return ": no one may, until an administrator changes the rule; make a new tag instead"
}

// HasTags reports whether any update is a tag.
func HasTags(updates []Update) bool {
	return slices.ContainsFunc(updates, func(u Update) bool { return strings.HasPrefix(u.Name, "refs/tags/") })
}
