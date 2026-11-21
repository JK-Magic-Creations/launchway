package storage

import (
	"context"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Archive serves git-upload-archive over SSH: the cache is made current, then
// git's own upload-archive answers on the streams of rpc. Nothing is written.
func (s *Store) Archive(ctx context.Context, repo string, rpc RPC) error {
	if err := s.ensureCurrent(ctx, repo); err != nil {
		return err
	}
	_, err := gitexec.Run(ctx, gitexec.Spec{GitDir: s.cacheDir(repo), Args: []string{"upload-archive", "."}, Stdin: rpc.In, Stdout: rpc.Out,
		IdleTimeout: 5 * time.Minute, Limits: s.o.Limits})
	return err
}
