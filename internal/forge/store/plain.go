package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// PlainConnectionName is the connection every watched project belongs to: the
// plain Git provider, today's watcher of a Git address.
const PlainConnectionName = "plain-git"

// Project is what the store needs to know of a watched project.
type Project struct {
	Name          string
	URL           string
	DefaultBranch string
}

// SyncPlain makes sure the plain connection exists and that every project is
// a repository on it, creating or updating rows. It never deletes: a project
// that left the configuration keeps its row (its history of events matters),
// and nothing about how the project is watched changes. It is safe to run
// at every start-up, and on several servers at once.
func (s *Store) SyncPlain(ctx context.Context, projects []Project) (forge.Connection, error) {
	conn, err := s.ConnectionByName(ctx, PlainConnectionName)
	if errors.Is(err, ErrNotFound) {
		conn, err = s.CreateConnection(ctx, forge.Connection{Name: PlainConnectionName, Kind: forge.KindPlain, Events: forge.EventsPolling, Enabled: true}, "startup")
		if errors.Is(err, ErrExists) { // another server did it first
			conn, err = s.ConnectionByName(ctx, PlainConnectionName)
		}
	}
	if err != nil {
		return conn, fmt.Errorf("the plain Git connection: %w", err)
	}
	for _, p := range projects {
		https, ssh := CleanAddress(p.URL)
		_, err := s.PutRepository(ctx, forge.Repository{
			Connection: conn.ID, Name: p.Name, Path: p.Name, DefaultBranch: p.DefaultBranch,
			Visibility: forge.Private, Clone: forge.CloneAddresses{HTTPS: https, SSH: ssh},
		}, p.Name)
		if err != nil {
			return conn, fmt.Errorf("the repository of project %s: %w", p.Name, err)
		}
	}
	return conn, nil
}
