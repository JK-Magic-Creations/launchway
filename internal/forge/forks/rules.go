// Package forks makes forks and template repositories of hosted repositories
// (docs/guide/forks.md and docs/guide/templates.md): the rules of who may fork
// what and how visible the fork may be, the fork itself (storage shares the
// parent's packs by reference, see internal/forge/storage/fork.go), keeping a
// fork current, and starting a new repository from a template as one commit.
//
// The package does not decide who a person is or whether they may read a
// repository: the API does that before it calls in, with the same permission
// checks as every other repository route.
package forks

import (
	"errors"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// Errors the API turns into answers.
var (
	// ErrForksOff: the repository (or its owner) does not allow forks.
	ErrForksOff = errors.New("forks are not allowed")
	// ErrRelation: the operation does not fit the repository's place in its
	// network (it is no fork, its parent is gone, ...).
	ErrRelation = errors.New("not possible for this repository's fork relationship")
	// ErrVisibility: the visibility asked for is wider than the rule allows.
	ErrVisibility = errors.New("visibility not allowed")
)

// Rank orders visibilities from the narrowest: a repository that is visible to
// more people has a higher rank.
func Rank(v forge.Visibility) int {
	switch v {
	case forge.Public:
		return 2
	case forge.Internal:
		return 1
	}
	return 0
}

// AllowForks decides whether a repository may be forked, from its own switch
// (-1 follows the default), its owner's switch (known when ownerSet) and its
// visibility. The repository's switch wins over the owner's, which wins over
// the default: on for a public repository, off for a private or internal one.
// why says in words where the answer came from.
func AllowForks(repoSwitch int, owner bool, ownerSet bool, vis forge.Visibility) (allow bool, why string) {
	switch {
	case repoSwitch == 0:
		return false, "the repository's own setting turns forks off"
	case repoSwitch == 1:
		return true, "the repository's own setting turns forks on"
	case ownerSet && !owner:
		return false, "its owner's setting turns forks off for all its repositories"
	case ownerSet && owner:
		return true, "its owner's setting turns forks on for all its repositories"
	case vis == forge.Public:
		return true, "forks are on by default for a public repository"
	}
	return false, "forks are off by default for a repository that is not public; the repository's administrator can turn them on"
}

// ForkVisibility is the visibility a fork gets: never wider than its parent's.
// A fork of a private repository is private. Asking for nothing gives the
// parent's own.
func ForkVisibility(parent, asked forge.Visibility) (forge.Visibility, error) {
	if asked == "" {
		return parent, nil
	}
	switch asked {
	case forge.Private, forge.Internal, forge.Public:
	default:
		return "", fmt.Errorf("%w: unknown visibility %q", forge.ErrInvalid, asked)
	}
	if Rank(asked) > Rank(parent) {
		return "", fmt.Errorf("%w: a fork cannot be more visible than its parent, which is %s", ErrVisibility, parent)
	}
	return asked, nil
}

// SyncAllowed says whether a fork may take changes from its parent: not from a
// parent that has become less visible than the fork, which would carry its
// newer commits into a wider audience than they were meant for.
func SyncAllowed(parent, fork forge.Visibility) error {
	if Rank(fork) > Rank(parent) {
		return fmt.Errorf("%w: the parent is %s now and this fork is %s; make the fork %s first", ErrVisibility, parent, fork, parent)
	}
	return nil
}

// Info is a repository's fork row with its place in the network.
type Info = store.ForkInfo

// RootOf is the repository at the top of the chain an Info belongs to.
func RootOf(i Info) string {
	if i.RootID != "" {
		return i.RootID
	}
	return i.ID
}

// SameNetwork reports whether two repositories are forks of one root (the root
// itself counts): a pull request may go between them.
func SameNetwork(a, b Info) bool { return RootOf(a) == RootOf(b) }
