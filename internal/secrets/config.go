package secrets

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Config maps store names to their settings. It is the runner.secrets
// section of the server's configuration, and the whole of the file given to
// --secrets-config.
type Config map[string]StoreConfig

// StoreConfig is one store. Type decides which other settings apply.
type StoreConfig struct {
	Type     string   `yaml:"type"`     // env, file or vault
	Projects []string `yaml:"projects"` // projects that may read it; empty: all
	Keys     []string `yaml:"keys"`     // keys that may be read; empty: all

	Dir string `yaml:"dir"` // file

	Address   string `yaml:"address"`    // vault: https://vault.example:8200
	Mount     string `yaml:"mount"`      // vault: the KV v2 mount; default secret
	Namespace string `yaml:"namespace"`  // vault: sent as X-Vault-Namespace
	CAFile    string `yaml:"ca_file"`    // vault: PEM certificates to trust
	TokenEnv  string `yaml:"token_env"`  // vault: log in with the token in this variable
	TokenFile string `yaml:"token_file"` // vault: or with the token in this file
	Role      string `yaml:"role"`       // vault: or with Kubernetes auth, as this role
	AuthPath  string `yaml:"auth_path"`  // vault: where Kubernetes auth is mounted; default kubernetes
	JWTFile   string `yaml:"jwt_file"`   // vault: the service account token; default DefaultJWTFile
}

// LoadConfig reads a store map from a YAML file. Relative paths in it are
// relative to the file.
func LoadConfig(file string) (Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	c.RelativeTo(filepath.Dir(file))
	return c, nil
}

// RelativeTo makes the relative file paths in c relative to dir.
func (c Config) RelativeTo(dir string) {
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	for name, s := range c {
		s.Dir, s.CAFile, s.TokenFile, s.JWTFile = abs(s.Dir), abs(s.CAFile), abs(s.TokenFile), abs(s.JWTFile)
		c[name] = s
	}
}

// Open checks every store's settings and returns a resolver with exactly
// those stores. Nothing is available that was not configured. It does not
// contact any store.
func Open(c Config) (*Resolver, error) {
	r := NewResolver()
	for _, name := range slices.Sorted(maps.Keys(c)) {
		s := c[name]
		st, err := newStore(name, s)
		if err != nil {
			return nil, fmt.Errorf("secret store %q: %w", name, err)
		}
		r.Add(name, st, s.Projects, s.Keys)
	}
	return r, nil
}

func newStore(name string, s StoreConfig) (Store, error) {
	if !pipeline.ValidStoreName(name) {
		return nil, errors.New("not a valid name: use lower-case letters, digits, hyphens and underscores")
	}
	for _, p := range append(append([]string{}, s.Projects...), s.Keys...) {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("%q is not a valid pattern", p)
		}
	}
	// Settings that belong to other types are mistakes; say so.
	given := map[string]bool{
		"dir": s.Dir != "", "address": s.Address != "", "mount": s.Mount != "", "namespace": s.Namespace != "",
		"ca_file": s.CAFile != "", "token_env": s.TokenEnv != "", "token_file": s.TokenFile != "", "role": s.Role != "",
		"auth_path": s.AuthPath != "", "jwt_file": s.JWTFile != "",
	}
	allowed := map[string][]string{
		"env":   nil,
		"file":  {"dir"},
		"vault": {"address", "mount", "namespace", "ca_file", "token_env", "token_file", "role", "auth_path", "jwt_file"},
	}
	fields, ok := allowed[s.Type]
	if !ok {
		return nil, fmt.Errorf("type %q is not known; use env, file or vault", s.Type)
	}
	for _, k := range slices.Sorted(maps.Keys(given)) {
		if given[k] && !slices.Contains(fields, k) {
			return nil, fmt.Errorf("%s does not apply to %s stores", k, s.Type)
		}
	}
	switch s.Type {
	case "env":
		return Env{}, nil
	case "file":
		if s.Dir == "" {
			return nil, errors.New("dir is required")
		}
		if fi, err := os.Stat(s.Dir); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("dir %s is not a directory", s.Dir)
		}
		return File{Dir: s.Dir}, nil
	}
	return newVault(s)
}

func newVault(s StoreConfig) (*Vault, error) {
	u, err := url.Parse(s.Address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("address %q is not an http or https URL", s.Address)
	}
	v := &Vault{
		addr: strings.TrimRight(s.Address, "/"), mount: strings.Trim(s.Mount, "/"), namespace: s.Namespace,
		tokenEnv: s.TokenEnv, tokenFile: s.TokenFile, role: s.Role, authPath: strings.Trim(s.AuthPath, "/"), jwtFile: s.JWTFile,
		now: time.Now,
	}
	if v.mount == "" {
		v.mount = "secret"
	}
	if v.authPath == "" {
		v.authPath = "kubernetes"
	}
	if !cleanPath(v.mount) || !cleanPath(v.authPath) {
		return nil, errors.New("mount and auth_path must be paths such as secret or kubernetes")
	}
	ways := 0
	for _, w := range []string{s.TokenEnv, s.TokenFile, s.Role} {
		if w != "" {
			ways++
		}
	}
	switch {
	case ways != 1:
		return nil, errors.New("give exactly one way to log in: token_env, token_file, or role for Kubernetes auth")
	case s.Role == "" && (s.AuthPath != "" || s.JWTFile != ""):
		return nil, errors.New("auth_path and jwt_file are for Kubernetes auth; add role")
	case s.TokenEnv != "" && os.Getenv(s.TokenEnv) == "":
		return nil, fmt.Errorf("token_env: the variable %s is not set", s.TokenEnv)
	}
	if v.role != "" && v.jwtFile == "" {
		v.jwtFile = DefaultJWTFile
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, fmt.Errorf("ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s holds no PEM certificates", s.CAFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	v.client = &http.Client{Transport: transport, Timeout: vaultTimeout}
	return v, nil
}
