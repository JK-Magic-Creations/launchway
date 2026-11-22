package work

import (
	"context"
	"slices"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// Reactions: a small fixed set, one row per person, target and reaction. Anyone who can see an item may react to it
// and to its comments; an agent does not. Counts and the caller's own marks come with every item and comment the API
// returns.

// Reactions is the set a person may choose from.
var Reactions = []string{"thumbs_up", "thumbs_down", "laugh", "hooray", "confused", "heart", "rocket", "eyes"}

// ReactionCount is how many people made a reaction, and whether the caller is one of them.
type ReactionCount struct {
	Reaction string `json:"reaction"`
	Count    int    `json:"count"`
	Mine     bool   `json:"mine"`
}

const (
	targetItem    = "item"
	targetComment = "comment"
)

func groupReactions(rows []store.ReactionRow) map[string][]ReactionCount {
	out := map[string][]ReactionCount{}
	for _, r := range rows {
		out[r.Target] = append(out[r.Target], ReactionCount{Reaction: r.Reaction, Count: r.Count, Mine: r.Mine})
	}
	// In the order of the fixed set, so that a client shows them the same way each time.
	for _, list := range out {
		slices.SortFunc(list, func(a, b ReactionCount) int {
			return slices.Index(Reactions, a.Reaction) - slices.Index(Reactions, b.Reaction)
		})
	}
	return out
}

// fillItemReactions puts the counts on items.
func (s *Store) fillItemReactions(ctx context.Context, c Caller, items []*Item) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	var all []store.ReactionRow
	for _, chunk := range chunks(ids, 400) {
		rows, err := store.ReactionCounts(ctx, s.db, targetItem, chunk, c.Name)
		if err != nil {
			return err
		}
		all = append(all, rows...)
	}
	by := groupReactions(all)
	for _, it := range items {
		it.Reactions = by[it.ID]
	}
	return nil
}

func (s *Store) fillCommentReactions(ctx context.Context, c Caller, cms []Comment) error {
	if len(cms) == 0 {
		return nil
	}
	ids := make([]string, len(cms))
	for i, cm := range cms {
		ids[i] = cm.ID
	}
	rows, err := store.ReactionCounts(ctx, s.db, targetComment, ids, c.Name)
	if err != nil {
		return err
	}
	by := groupReactions(rows)
	for i := range cms {
		cms[i].Reactions = by[cms[i].ID]
	}
	return nil
}

// React adds (on) or removes (not on) the caller's reaction to an item (target is its reference) or to a comment
// (target is its id), and returns the counts that now stand on the target.
func (s *Store) React(ctx context.Context, c Caller, comment bool, target, reaction string, on bool) ([]ReactionCount, error) {
	if !slices.Contains(Reactions, reaction) {
		return nil, invalid("a reaction is one of %v", Reactions)
	}
	if c.Agent != nil {
		return nil, forbidden("an agent does not react")
	}
	kind, id := targetItem, ""
	var itemRef string
	if comment {
		kind = targetComment
		itemID, ok, err := store.WorkCommentItem(ctx, s.db, target)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, notFound("no comment " + target)
		}
		itemRef, id = itemID, target
	} else {
		itemRef = target
	}
	it, err := s.Get(ctx, c, itemRef) // not found when the caller may not see it
	if err != nil {
		if comment {
			return nil, notFound("no comment " + target)
		}
		return nil, err
	}
	if !comment {
		id = it.ID
	}
	if on {
		_, err = store.ReactionSet(ctx, s.db, kind, id, c.Name, reaction, ms(s.now()))
	} else {
		_, err = store.ReactionClear(ctx, s.db, kind, id, c.Name, reaction)
	}
	if err != nil {
		return nil, err
	}
	rows, err := store.ReactionCounts(ctx, s.db, kind, []string{id}, c.Name)
	if err != nil {
		return nil, err
	}
	out := groupReactions(rows)[id]
	if out == nil {
		out = []ReactionCount{}
	}
	return out, nil
}
