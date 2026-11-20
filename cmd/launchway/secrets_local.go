package main

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// localSecrets is openSecrets for 'launchway run' on your own machine, where
// your environment is yours to use: it adds the env store. A server or
// shared runner must never do this, or any pipeline could read the runner's
// environment.
func localSecrets(file string) (*secrets.Resolver, error) {
	r, err := openSecrets(file)
	if err != nil {
		return nil, err
	}
	return r.AddLocalEnv(), nil
}

// openSecrets reads and checks the secret stores in file, if one is given.
func openSecrets(file string) (*secrets.Resolver, error) {
	if file == "" {
		return secrets.NewResolver(), nil
	}
	c, err := secrets.LoadConfig(file)
	if err != nil {
		return nil, err
	}
	r, err := secrets.Open(c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return r, nil
}
