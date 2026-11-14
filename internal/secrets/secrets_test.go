package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

var ctx = context.Background()

// wantNotFound fails unless err is a *NotFoundError mentioning want.
func wantNotFound(t *testing.T, err error, want string) {
	t.Helper()
	var nf *NotFoundError
	if !errors.As(err, &nf) || IsUnavailable(err) {
		t.Fatalf("got %T %v, want a NotFoundError", err, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("got %q, want it to mention %q", err, want)
	}
}

func TestEnvStore(t *testing.T) {
	t.Setenv("LW_TEST_SECRET", "s3cret-value")
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "runner-token")
	v, err := Env{}.Get(ctx, "LW_TEST_SECRET", "")
	if err != nil || v != "s3cret-value" {
		t.Fatalf("got %q, %v", v, err)
	}
	_, err = Env{}.Get(ctx, "LW_TEST_MISSING", "")
	wantNotFound(t, err, `no environment variable "LW_TEST_MISSING"`)
	_, err = Env{}.Get(ctx, "LW_TEST_SECRET", "password")
	wantNotFound(t, err, "no fields")
	_, err = Env{}.Get(ctx, "LAUNCHWAY_RUNNER_TOKEN", "")
	wantNotFound(t, err, "LAUNCHWAY_")
}

func TestFileStore(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "token"), "abc-123\n")
	write(filepath.Join(dir, "crlf"), "line\r\n")
	write(filepath.Join(dir, "sub", "key"), "nested")
	write(filepath.Join(outside, "stolen"), "not yours")
	// Kubernetes mounts a secret as symbolic links into a hidden directory.
	write(filepath.Join(dir, "..2026_11_14", "password"), "hunter22\n")
	must(t, os.Symlink("..2026_11_14", filepath.Join(dir, "..data")))
	must(t, os.Symlink("..data/password", filepath.Join(dir, "password")))
	must(t, os.Symlink(filepath.Join(outside, "stolen"), filepath.Join(dir, "link-out")))

	f := File{Dir: dir}
	for key, want := range map[string]string{"token": "abc-123", "crlf": "line", "sub/key": "nested", "password": "hunter22"} {
		if v, err := f.Get(ctx, key, ""); err != nil || v != want {
			t.Errorf("%s: got %q, %v; want %q", key, v, err, want)
		}
	}
	_, err := f.Get(ctx, "missing", "")
	wantNotFound(t, err, `no file "missing"`)
	for _, key := range []string{"../" + filepath.Base(outside) + "/stolen", filepath.Join(outside, "stolen"), "sub/../../x", ""} {
		_, err = f.Get(ctx, key, "")
		wantNotFound(t, err, "relative file name inside")
	}
	_, err = f.Get(ctx, "link-out", "")
	wantNotFound(t, err, "escapes")
	_, err = f.Get(ctx, "token", "field")
	wantNotFound(t, err, "no fields")
}

type fakeStore struct{ calls int }

func (f *fakeStore) Get(_ context.Context, key, field string) (string, error) {
	f.calls++
	return key + "#" + field, nil
}

func TestResolverAllowLists(t *testing.T) {
	r := NewResolver()
	fs := &fakeStore{}
	r.Add("vault", fs, []string{"payments", "web-*"}, []string{"apps/*/robot"})
	ref := func(key string) pipeline.SecretRef { return pipeline.SecretRef{Store: "vault", Key: key, Field: "f"} }

	if v, err := r.Lookup(ctx, "web-shop", ref("apps/harbor/robot")); err != nil || v != "apps/harbor/robot#f" {
		t.Fatalf("got %q, %v", v, err)
	}
	_, err := r.Lookup(ctx, "other", ref("apps/harbor/robot"))
	wantNotFound(t, err, `store "vault" does not allow project "other" (projects: payments, web-*)`)
	_, err = r.Lookup(ctx, "payments", ref("infra/root"))
	wantNotFound(t, err, `does not allow key "infra/root" (keys: apps/*/robot)`)
	_, err = r.Lookup(ctx, "payments", pipeline.SecretRef{Store: "nope", Key: "k"})
	wantNotFound(t, err, `no secret store called "nope"`)
	if fs.calls != 1 {
		t.Fatalf("the store was asked %d times; refused requests must not reach it", fs.calls)
	}
	// There is no env store unless one is configured, or the caller is a
	// local run that adds it.
	t.Setenv("LW_TEST_SECRET", "from-env")
	_, err = r.Lookup(ctx, "any", pipeline.SecretRef{Store: "env", Key: "LW_TEST_SECRET"})
	wantNotFound(t, err, `no secret store called "env"`)
	r.AddLocalEnv()
	if v, err := r.Lookup(ctx, "any", pipeline.SecretRef{Store: "env", Key: "LW_TEST_SECRET"}); err != nil || v != "from-env" {
		t.Fatalf("env: %q, %v", v, err)
	}
}

func TestOpenConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LW_TEST_VAULT_TOKEN", "tok")
	good := Config{
		"env":   {Type: "env", Projects: []string{"a"}},
		"files": {Type: "file", Dir: dir, Keys: []string{"*"}},
		"vault": {Type: "vault", Address: "https://vault.example:8200", TokenEnv: "LW_TEST_VAULT_TOKEN"},
		"k8s":   {Type: "vault", Address: "http://bao:8200", Role: "ci", Mount: "/kv/", Namespace: "team"},
	}
	r, err := Open(good)
	if err != nil {
		t.Fatal(err)
	}
	// Redefining env replaces the default, allow-list included.
	_, err = r.Lookup(ctx, "b", pipeline.SecretRef{Store: "env", Key: "HOME"})
	wantNotFound(t, err, "does not allow project")
	k := r.stores["k8s"].store.(*Vault)
	if k.mount != "kv" || k.authPath != "kubernetes" || k.jwtFile != DefaultJWTFile {
		t.Fatalf("vault defaults: %+v", k)
	}

	bad := []struct {
		name string
		c    StoreConfig
		want string
	}{
		{"Bad", StoreConfig{Type: "env"}, "not a valid name"},
		{"s", StoreConfig{Type: "aws"}, `type "aws" is not known`},
		{"s", StoreConfig{}, `type "" is not known`},
		{"s", StoreConfig{Type: "env", Dir: dir}, "dir does not apply to env stores"},
		{"s", StoreConfig{Type: "file"}, "dir is required"},
		{"s", StoreConfig{Type: "file", Dir: filepath.Join(dir, "nope")}, "not a directory"},
		{"s", StoreConfig{Type: "file", Dir: dir, Role: "x"}, "role does not apply to file stores"},
		{"s", StoreConfig{Type: "env", Projects: []string{"[x"}}, "not a valid pattern"},
		{"s", StoreConfig{Type: "vault", TokenEnv: "LW_TEST_VAULT_TOKEN"}, "not an http or https URL"},
		{"s", StoreConfig{Type: "vault", Address: "vault:8200", TokenEnv: "LW_TEST_VAULT_TOKEN"}, "not an http or https URL"},
		{"s", StoreConfig{Type: "vault", Address: "http://v"}, "exactly one way to log in"},
		{"s", StoreConfig{Type: "vault", Address: "http://v", TokenEnv: "LW_TEST_VAULT_TOKEN", Role: "r"}, "exactly one way to log in"},
		{"s", StoreConfig{Type: "vault", Address: "http://v", TokenFile: "/t", JWTFile: "/j"}, "add role"},
		{"s", StoreConfig{Type: "vault", Address: "http://v", TokenEnv: "LW_TEST_UNSET_VARIABLE"}, "is not set"},
		{"s", StoreConfig{Type: "vault", Address: "http://v", Role: "r", Mount: "a/../b"}, "mount and auth_path"},
		{"s", StoreConfig{Type: "vault", Address: "http://v", Role: "r", CAFile: filepath.Join(dir, "none.pem")}, "ca_file"},
	}
	for _, b := range bad {
		_, err := Open(Config{b.name: b.c})
		if err == nil || !strings.Contains(err.Error(), b.want) || !strings.Contains(err.Error(), `secret store "`+b.name+`"`) {
			t.Errorf("%+v: got %v, want it to mention %q", b.c, err, b.want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	must(t, os.Mkdir(filepath.Join(dir, "mounted"), 0o755))
	file := filepath.Join(dir, "secrets.yaml")
	must(t, os.WriteFile(file, []byte(`
files: { type: file, dir: mounted, projects: [web] }
vault: { type: vault, address: "http://127.0.0.1:1", token_file: token }
`), 0o600))
	c, err := LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if c["files"].Dir != filepath.Join(dir, "mounted") || c["vault"].TokenFile != filepath.Join(dir, "token") {
		t.Fatalf("paths not made relative to the file: %+v", c)
	}
	if _, err := Open(c); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(file, []byte("files: { type: file, directory: x }\n"), 0o600))
	if _, err := LoadConfig(file); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("unknown setting accepted: %v", err)
	}
}

func TestMasker(t *testing.T) {
	m, short := NewMasker(map[string]string{
		"TOKEN": "s3cret-value",
		"LONG":  "s3cret-value-longer",
		"CERT":  "-----BEGIN-----\r\nMIIBszCCAVmg\n\nok\n-----END-----\n",
		"PIN":   "123",
		"EMPTY": "",
	})
	for in, want := range map[string]string{
		"token=s3cret-value!":          "token=***!",
		"s3cret-value-longer":          "***",
		"xs3cret-values3cret-valuex":   "x******x",
		"cert line MIIBszCCAVmg here":  "cert line *** here",
		"-----BEGIN-----":              "***",
		"pin 123 and ok stay readable": "pin 123 and ok stay readable",
	} {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.Join(short, ",") != "CERT,PIN" {
		t.Fatalf("too short to mask: %v", short)
	}
	var none *Masker
	if none.Mask("x") != "x" {
		t.Fatal("a nil masker changed the text")
	}
	if m, _ := NewMasker(nil); m != nil {
		t.Fatal("a masker with no values should be nil")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
