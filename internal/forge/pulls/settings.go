package pulls

import (
	"context"
	"encoding/json"
	"database/sql"
	"errors"
	"strings"
)

// Merge methods.
const (
	MethodMerge  = "merge"
	MethodSquash = "squash"
	MethodRebase = "rebase"
)

// Methods is the set, in a fixed order.
var Methods = []string{MethodMerge, MethodSquash, MethodRebase}

// Settings are a repository's pull request settings.
type Settings struct {
	AllowMerge  bool `json:"allow_merge"`
	AllowSquash bool `json:"allow_squash"`
	AllowRebase bool `json:"allow_rebase"`
	// DefaultMethod is the method the merge box offers first.
	DefaultMethod string `json:"default_method"`
	// DeleteBranch deletes the source branch after a merge unless the person
	// merging says otherwise.
	DeleteBranch bool `json:"delete_branch_after_merge"`
	// DismissStale dismisses approvals when new commits arrive.
	DismissStale bool `json:"dismiss_stale_approvals"`
	// MergeTemplate and SquashTemplate are the messages of merge and squash
	// commits; see ExpandTemplate for what they may contain.
	MergeTemplate  string `json:"merge_message_template"`
	SquashTemplate string `json:"squash_message_template"`
	// AssignReviewers says how reviewers are chosen when a pull request is
	// ready for review: "" (not at all), "owners" (every owner of the changed
	// files), "round_robin" or "least_loaded" (one person for each file's
	// owners, by turns or by fewest open requests; a group owner is always
	// asked as a group).
	AssignReviewers string `json:"assign_reviewers"`
}

// Ways of assigning reviewers.
const (
	AssignOwners      = "owners"
	AssignRoundRobin  = "round_robin"
	AssignLeastLoaded = "least_loaded"
)

// DefaultSettings are used until a repository sets its own.
var DefaultSettings = Settings{AllowMerge: true, AllowSquash: true, AllowRebase: true, DefaultMethod: MethodMerge, DismissStale: true,
	MergeTemplate: "Merge pull request #{number} from {source}\n\n{title}", SquashTemplate: "{title} (#{number})\n\n{body}"}

// Allowed lists the methods the settings allow, in the fixed order.
func (s Settings) Allowed() []string {
	var out []string
	for _, m := range Methods {
		if s.allows(m) {
			out = append(out, m)
		}
	}
	return out
}

func (s Settings) allows(m string) bool {
	switch m {
	case MethodMerge:
		return s.AllowMerge
	case MethodSquash:
		return s.AllowSquash
	case MethodRebase:
		return s.AllowRebase
	}
	return false
}

// Validate checks settings a person sent.
func (s *Settings) Validate() error {
	if !s.AllowMerge && !s.AllowSquash && !s.AllowRebase {
		return invalid("at least one way of merging must be allowed, or nothing could ever be merged")
	}
	if s.DefaultMethod == "" {
		s.DefaultMethod = MethodMerge
	}
	if !s.allows(s.DefaultMethod) {
		return invalid("the default method %q is not one of the allowed methods (%s)", s.DefaultMethod, strings.Join(s.Allowed(), ", "))
	}
	switch s.AssignReviewers {
	case "", AssignOwners, AssignRoundRobin, AssignLeastLoaded:
	default:
		return invalid("reviewers are assigned by owners, round_robin or least_loaded, or not at all")
	}
	if len(s.MergeTemplate) > 4096 || len(s.SquashTemplate) > 4096 {
		return invalid("a message template is at most 4096 characters")
	}
	return nil
}

// Settings returns a repository's settings.
func (s *Store) Settings(ctx context.Context, repoID string) (Settings, error) {
	var m, sq, rb, del, dis int
	var extra string
	var out Settings
	err := s.db.QueryRow(ctx, `SELECT allow_merge, allow_squash, allow_rebase, default_method, delete_branch, dismiss_stale, merge_template, squash_template, extra
		FROM repo_pull_settings WHERE repo_id = ?`, repoID).Scan(&m, &sq, &rb, &out.DefaultMethod, &del, &dis, &out.MergeTemplate, &out.SquashTemplate, &extra)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings, nil
	}
	if err != nil {
		return out, err
	}
	var ex struct {
		Assign string `json:"assign"`
	}
	_ = json.Unmarshal([]byte(extra), &ex)
	out.AssignReviewers = ex.Assign
	out.AllowMerge, out.AllowSquash, out.AllowRebase, out.DeleteBranch, out.DismissStale = m != 0, sq != 0, rb != 0, del != 0, dis != 0
	if out.MergeTemplate == "" {
		out.MergeTemplate = DefaultSettings.MergeTemplate
	}
	if out.SquashTemplate == "" {
		out.SquashTemplate = DefaultSettings.SquashTemplate
	}
	return out, nil
}

// SetSettings replaces a repository's settings.
func (s *Store) SetSettings(ctx context.Context, repoID string, in Settings, by string) error {
	if err := in.Validate(); err != nil {
		return err
	}
	eb, _ := json.Marshal(map[string]string{"assign": in.AssignReviewers})
	extra := string(eb)
	_, err := s.db.Exec(ctx, `INSERT INTO repo_pull_settings(repo_id, allow_merge, allow_squash, allow_rebase, default_method, delete_branch, dismiss_stale, merge_template, squash_template, updated_at, updated_by, extra)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(repo_id) DO UPDATE SET allow_merge = excluded.allow_merge, allow_squash = excluded.allow_squash, allow_rebase = excluded.allow_rebase,
		default_method = excluded.default_method, delete_branch = excluded.delete_branch, dismiss_stale = excluded.dismiss_stale, merge_template = excluded.merge_template,
		squash_template = excluded.squash_template, updated_at = excluded.updated_at, updated_by = excluded.updated_by, extra = excluded.extra`,
		repoID, b2i(in.AllowMerge), b2i(in.AllowSquash), b2i(in.AllowRebase), in.DefaultMethod, b2i(in.DeleteBranch), b2i(in.DismissStale), in.MergeTemplate, in.SquashTemplate, ms(s.now()), by, extra)
	return err
}
