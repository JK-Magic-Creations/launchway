package executor

import (
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

// A cache is restored by every run that may read its scope, so a credential
// kept in one reaches more places than it was written for: a cached
// ~/.npmrc, a Maven settings.xml with a server password or a docker
// config with an auth token is handed to whatever runs next. A cache that
// holds a file that looks like a credential is therefore not saved at all.
//
// Some names are credentials whatever they hold (.netrc, .git-credentials,
// private keys, .env); for others (.npmrc, settings.xml, config.json of
// docker, ...) the file is read, because the same name holds harmless
// settings in most projects. Environment references such as ${NPM_TOKEN} are
// not credentials.

// CredentialFinding is a staged file that looks like it holds a credential.
type CredentialFinding struct {
	Path string
	// Why is plain words: what kind of file it is.
	Why string
}

type credentialRule struct {
	// match says whether the rule applies to the file at this path.
	match func(p, base string) bool
	// content, if set, must match the file's first bytes for it to count.
	content *regexp.Regexp
	why     string
}

func baseIn(names ...string) func(string, string) bool {
	return func(_, base string) bool {
		for _, n := range names {
			if base == n {
				return true
			}
		}
		return false
	}
}

func globIn(globs ...string) func(string, string) bool {
	return func(_, base string) bool {
		for _, g := range globs {
			if ok, _ := path.Match(g, base); ok {
				return true
			}
		}
		return false
	}
}

func inDir(dir string, names ...string) func(string, string) bool {
	return func(p, base string) bool {
		if !strings.Contains("/"+path.Dir(p)+"/", "/"+dir+"/") {
			return false
		}
		for _, n := range names {
			if base == n {
				return true
			}
		}
		return false
	}
}

func either(fs ...func(string, string) bool) func(string, string) bool {
	return func(p, base string) bool {
		for _, f := range fs {
			if f(p, base) {
				return true
			}
		}
		return false
	}
}

// val matches an assignment whose value is something, and is not an
// environment reference such as ${NPM_TOKEN}.
const val = `\s*[=:]\s*["']?[^\s"'$]`

var credentialRules = []credentialRule{
	{match: baseIn(".netrc", "_netrc", ".git-credentials", ".pgpass", ".htpasswd", ".dockercfg", ".my.cnf", "settings-security.xml", "credentials.tfrc.json"),
		why: "a credentials file"},
	{match: either(baseIn("id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"), globIn("*_rsa", "*_ed25519", "*_ecdsa", "*.ppk")), why: "a private key"},
	{match: globIn("*.kdbx", "*.p12", "*.pfx", "*.jks", "*.keystore"), why: "a key store"},
	{match: func(_, base string) bool {
		if base == ".env" || strings.HasPrefix(base, ".env.") {
			switch strings.TrimPrefix(base, ".env.") {
			case "example", "sample", "template", "dist", "defaults":
				return false
			}
			return true
		}
		return false
	}, why: "an environment file, which usually holds secrets"},
	{match: either(baseIn("terraform.tfvars", "service-account.json", "secrets.json", "secrets.yaml", "secrets.yml"), globIn("*.tfvars")), why: "a file of secret values"},
	{match: globIn("*.pem", "*.key"), content: regexp.MustCompile(`PRIVATE KEY-----`), why: "a private key"},
	{match: baseIn(".npmrc"), content: regexp.MustCompile(`(?m)^[^#;\n]*(_authToken|_auth|_password)` + val), why: "an .npmrc with an auth token or password"},
	{match: baseIn(".yarnrc", ".yarnrc.yml"), content: regexp.MustCompile(`(?i)(npmAuthToken|npmAuthIdent|_authToken)` + val), why: "a yarn configuration with an auth token"},
	{match: baseIn(".pypirc"), content: regexp.MustCompile(`(?mi)^\s*password` + val), why: "a .pypirc with a password"},
	{match: globIn("pip.conf", "pip.ini"), content: regexp.MustCompile(`(?i)(://[^/\s:@]+:[^/\s@$]+@|password` + val + `)`), why: "a pip configuration with credentials in an index URL"},
	{match: baseIn("settings.xml"), content: regexp.MustCompile(`(?s)<(password|passphrase|privateKey)>\s*[^\s<$]`), why: "a Maven settings.xml with a server password"},
	{match: baseIn("gradle.properties"), content: regexp.MustCompile(`(?mi)^[^#\n]*(password|token|secret|apikey|api_key)[^=:\n]*` + val), why: "a gradle.properties with a password or token"},
	{match: baseIn("auth.json"), content: regexp.MustCompile(`(-oauth|-token|http-basic|bearer)`), why: "a Composer auth.json with a token"},
	{match: inDir(".docker", "config.json"), content: regexp.MustCompile(`"(auth|identitytoken|registrytoken)"\s*:\s*"[^"]`), why: "a Docker config with registry credentials"},
	{match: inDir(".aws", "credentials", "config"), content: regexp.MustCompile(`(?i)(aws_secret_access_key|aws_session_token)` + val), why: "AWS credentials"},
	{match: inDir(".cargo", "credentials", "credentials.toml"), content: regexp.MustCompile(`(?m)^\s*token\s*=`), why: "a Cargo registry token"},
	{match: baseIn("credentials.json"), content: regexp.MustCompile(`"(private_key|client_secret|refresh_token|access_token)"`), why: "a credentials file"},
	{match: either(baseIn("kubeconfig"), globIn("*.kubeconfig"), inDir(".kube", "config")), content: regexp.MustCompile(`(?m)^\s*(token|client-key-data|password|auth-provider)\b`), why: "a kubeconfig with a token or key"},
}

// maxCredentialRead is how much of a file is read to look for credentials.
const maxCredentialRead = 256 << 10

// CredentialFiles returns the staged files that look like they hold a
// credential, with why. Files named by a rule that needs the content are
// read (the first 256 KiB).
func CredentialFiles(files []StagedFile) []CredentialFinding {
	var out []CredentialFinding
	for _, f := range files {
		base := path.Base(f.Path)
		if strings.HasSuffix(base, ".pub") {
			continue
		}
		for _, r := range credentialRules {
			if !r.match(f.Path, base) {
				continue
			}
			if r.content != nil && !contentMatches(f.Local, r.content) {
				continue
			}
			out = append(out, CredentialFinding{Path: f.Path, Why: r.why})
			break
		}
	}
	return out
}

func contentMatches(local string, re *regexp.Regexp) bool {
	fh, err := os.Open(local)
	if err != nil {
		return false
	}
	defer fh.Close()
	b, _ := io.ReadAll(io.LimitReader(fh, maxCredentialRead))
	return re.Match(b)
}
