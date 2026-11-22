package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/source"
)

// cmdSelfCheck runs a check of what the shipped image needs, inside the image (see make image-build-check). It is
// not part of the help: it exists so that a need for a shell, or a program the image does not have, fails a build
// and not a poll in production.
func cmdSelfCheck(args []string) error {
	if len(args) != 1 || args[0] != "hosted-read" {
		return fmt.Errorf("usage: launchway self-check hosted-read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := source.SelfCheckHostedRead(ctx, os.TempDir()); err != nil {
		return fmt.Errorf("a repository kept in this server's own storage cannot be read here: %w", err)
	}
	fmt.Println("self-check hosted-read: ok")
	return nil
}
