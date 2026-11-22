package signing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

type gitReader struct {
	store *storage.Store
	dir   string
}

func (g gitReader) run(ctx context.Context, args ...string) (string, error) {
	res, err := gitexec.Run(ctx, gitexec.Spec{GitDir: g.dir, Args: args, Timeout: time.Minute, Limits: g.store.Limits(), MaxOutput: 4 << 20})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(ee.Stderr))
		}
		return "", err
	}
	return res.Stdout, nil
}
