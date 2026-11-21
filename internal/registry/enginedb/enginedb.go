// Package enginedb adapts the engine's database to what the registry needs
// of one (registry.DB), so that the registry stays free of the engine.
package enginedb

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/registry"
)

type db struct{ *engine.Database }

type tx struct{ *engine.Tx }

// New wraps the engine's database.
func New(d *engine.Database) registry.DB { return db{d} }

func (d db) Begin(ctx context.Context) (registry.Tx, error) {
	t, err := d.Database.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return tx{t}, nil
}
