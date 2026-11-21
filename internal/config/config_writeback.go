package config

import "github.com/jk-magic-creations/launchway/internal/engine"

// WriteBackConfig is an environment's write-back to a hosted repository.
type WriteBackConfig struct {
	Repo   string `yaml:"repo"`
	Mode   string `yaml:"mode"`
	Merge  string `yaml:"merge"`
	Method string `yaml:"method"`
}

func (w *WriteBackConfig) engine() *engine.WriteBack {
	if w == nil {
		return nil
	}
	return &engine.WriteBack{Repo: w.Repo, Mode: w.Mode, Merge: w.Merge, Method: w.Method}
}
