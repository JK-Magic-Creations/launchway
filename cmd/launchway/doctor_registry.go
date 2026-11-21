package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/jk-magic-creations/launchway/internal/config"
)

// registryEncryptionLine is what the registry's blobs are protected by in
// this release, said wherever the registry is described.
const registryEncryptionLine = "Launchway does not encrypt the registry's blobs itself in this release (images, layers, charts are stored as pushed, like artifacts): " +
	"encrypt the volume or database that holds the artifact store, and the S3 bucket with server-side encryption. Per-object encryption under the installation's keys is the next slice."

type registryReport struct {
	Enabled          bool     `json:"enabled"`
	Store            string   `json:"store"`
	SignIn           bool     `json:"sign_in"`
	PublicURL        string   `json:"public_url,omitempty"`
	BlobsEncrypted   bool     `json:"blobs_encrypted_by_launchway"`
	EncryptionNote   string   `json:"encryption_note"`
	SharedStoreNeeds string   `json:"shared_store,omitempty"`
	Problems         []string `json:"problems,omitempty"`
}

func checkRegistry(cfg *config.Config) registryReport {
	r := registryReport{Enabled: cfg.Registry.On(), Store: cfg.Artifacts.Store, SignIn: cfg.Auth.Mode != config.AuthNone, PublicURL: cfg.Registry.PublicURL,
		EncryptionNote: registryEncryptionLine}
	if r.PublicURL == "" {
		r.PublicURL = cfg.Forge.PublicURL
	}
	if r.Store == "" {
		r.Store = "default (database on PostgreSQL, a directory on SQLite)"
	}
	if !r.Enabled {
		return r
	}
	if !r.SignIn {
		r.Problems = append(r.Problems, "sign-in is off (auth.mode: none), so the registry answers 503: it would be open to everyone who can reach the server. Turn sign-in on.")
	}
	if r.PublicURL == "" {
		r.Problems = append(r.Problems, "no registry.public_url (or forge.public_url): the registry address is taken from each request, so image steps and pull commands may name the wrong host behind a proxy.")
	}
	if cfg.Artifacts.Store == "filesystem" {
		r.SharedStoreNeeds = "the artifact store is a directory: several servers need it on a shared filesystem, or an s3 or database store"
	}
	return r
}

func (r registryReport) lines() []string {
	var out []string
	if !r.Enabled {
		return []string{"Registry: off (registry.enabled is false). Nothing is deleted."}
	}
	out = append(out, fmt.Sprintf("Registry: on; blobs in the artifact store (%s)", r.Store))
	if r.PublicURL != "" {
		out = append(out, "  Address ................ "+r.PublicURL)
	}
	out = append(out, "  Sign-in required ....... "+map[bool]string{true: "yes (there is no anonymous registry)", false: "NO: the registry is refusing every request"}[r.SignIn])
	out = append(out, "  Blobs encrypted at rest . no: "+r.EncryptionNote)
	if r.SharedStoreNeeds != "" {
		out = append(out, "  Note ................... "+r.SharedStoreNeeds)
	}
	for _, p := range r.Problems {
		out = append(out, "  Problem ................ "+p)
	}
	return out
}

// doctorRegistry says whether the built-in registry would work as configured,
// and what protects its blobs at rest.
func doctorRegistry(args []string) error {
	fs := flag.NewFlagSet("doctor registry", flag.ExitOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	asJSON := fs.Bool("json", false, "print JSON")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	r := checkRegistry(cfg)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"registry": r})
	}
	for _, l := range r.lines() {
		fmt.Println(l)
	}
	if len(r.Problems) > 0 {
		return exitErr(1)
	}
	return nil
}
