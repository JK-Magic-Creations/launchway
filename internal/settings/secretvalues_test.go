package settings

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A secret in an address would land in the append-only history, the
// export and every diff: the one validation path refuses it.
func TestAddressesCarryNoCredentials(t *testing.T) {
	for _, tc := range []struct {
		kind, spec string
		bad        bool
	}{
		{"project", `{"repo":"https://user:tok3n@git.example.test/a.git"}`, true},
		{"project", `{"repo":"https://tok3n@git.example.test/a.git"}`, true},
		{"project", `{"repo":"https://git.example.test/a.git?access_token=abc"}`, true},
		{"project", `{"repo":"git:s3cret@git.example.test:a.git"}`, true},
		{"project", `{"repo":"ssh://git:pw@git.example.test/a.git"}`, true},
		{"project", `{"repo":"https://git.example.test/a.git"}`, false},
		{"project", `{"repo":"git@git.example.test:a.git"}`, false},
		{"project", `{"repo":"ssh://git@git.example.test/a.git"}`, false},
		{"library", `{"repo":"https://u:t@git.example.test/l.git"}`, true},
		{"library", `{"repo":"https://git.example.test/l.git?private_token=x"}`, true},
		{"library", `{"repo":"https://git.example.test/l.git?ref=main"}`, false},
		{"model", `{"provider":"openai","model":"m","endpoint":"https://u:t@ollama.example.test/v1"}`, true},
		{"model", `{"provider":"openai","model":"m","endpoint":"https://ollama.example.test/v1","headers":{"Authorization":"Bearer abc"}}`, true},
		{"model", `{"provider":"openai","model":"m","endpoint":"https://ollama.example.test/v1","headers":{"X-Team":"core"}}`, false},
	} {
		k := kindByName(tc.kind)
		v, _, err := decodeSpec(k, "x", []byte(tc.spec))
		if err != nil {
			t.Fatalf("%s %s: %v", tc.kind, tc.spec, err)
		}
		err = checkOwn(k, v)
		switch {
		case tc.bad && err == nil:
			t.Errorf("%s %s: accepted", tc.kind, tc.spec)
		case tc.bad && !strings.Contains(err.Error(), "reference"):
			t.Errorf("%s: the message does not say to use a reference: %v", tc.spec, err)
		case tc.bad && (strings.Contains(err.Error(), "tok3n") || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "Bearer abc")):
			t.Errorf("%s: the message repeats the secret: %v", tc.spec, err)
		case !tc.bad && err != nil:
			t.Errorf("%s %s: refused: %v", tc.kind, tc.spec, err)
		}
	}
}

// valueLooksSecret: a setting named like a secret holds a secret value
// unless it is a reference (ends in _env or _file, or is a SecretRef).
var secretName = []string{"password", "passwd", "secret", "token", "apikey", "api_key", "credential", "authorization", "private_key"}

// The general guard: no kind's spec has a field that looks like it holds a
// secret value. A new field named like a secret that is a string must be a
// reference (name ending in _env or _file, or a struct such as a secret-store
// reference) or be listed here with the reason it is not a value.
func TestNoKindStoresASecretValue(t *testing.T) {
	notSecrets := map[string]string{
		"installation.tokens.max_days":      "a number of days",
		"model.max_tokens":                  "a number",
		"model.tokens_per_day":              "a number",
		"model.max_tokens_field":            "the name of a request field",
		"model.price.long_input_tokens":     "a number",
		"model.key":                         "a reference to a secret store",
		"project.git_webhook.secret_env":    "the name of an environment variable",
		"project.git_webhook.secret_file":   "a file name",
		"project.webhooks.token_env":        "the name of an environment variable",
		"project.webhooks.token_file":       "a file name",
		"project.webhooks.hmac_secret_env":  "the name of an environment variable",
		"project.webhooks.hmac_secret_file": "a file name",
		"runner-pool.token_env":             "the name of an environment variable",
		"runner-pool.token_file":            "a file name",
		"installation.mcp.oauth.scopes":     "scopes, not credentials",
	}
	// Free-form maps of strings are where a value can hide; each is checked
	// by the validation path or is documented not to be secret.
	freeMaps := map[string]string{
		"model.headers":                      "checkHeaders refuses credential headers",
		"project.pipeline.with":              "template parameters; the pipeline reads secrets by reference only",
		"project.default_pipeline.with":      "template parameters; the pipeline reads secrets by reference only",
		"organisation.default_pipeline.with": "template parameters; the pipeline reads secrets by reference only",
		"unit.default_pipeline.with":         "template parameters; the pipeline reads secrets by reference only",
		"installation.terms.overrides":       "display labels: keys must be known term names, values at most 40 characters without markup (Terms.check)",
	}
	seenMaps := map[string]bool{}
	for _, k := range kinds {
		var walk func(rt reflect.Type, path string, depth int)
		walk = func(rt reflect.Type, path string, depth int) {
			for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
				rt = rt.Elem()
			}
			if rt.Kind() == reflect.Map {
				if rt.Elem().Kind() == reflect.String {
					if _, ok := freeMaps[path]; !ok {
						t.Errorf("%s: a free-form map of strings; list it in freeMaps with how a secret is kept out", path)
					}
					seenMaps[path] = true
					return
				}
				rt = rt.Elem()
				for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
					rt = rt.Elem()
				}
			}
			if rt == reflect.TypeOf(yaml.Node{}) || rt.Kind() != reflect.Struct || depth > 8 {
				return
			}
			for i := 0; i < rt.NumField(); i++ {
				f := rt.Field(i)
				if !f.IsExported() {
					continue
				}
				name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
				if name == "" {
					name = strings.ToLower(f.Name)
				}
				p := path + "." + name
				low := strings.ToLower(name)
				base := f.Type
				for base.Kind() == reflect.Pointer || base.Kind() == reflect.Slice {
					base = base.Elem()
				}
				if base.Kind() != reflect.String && base.Kind() != reflect.Map {
					low = "" // a number, a switch or a group of settings, not a value
				}
				for _, w := range secretName {
					if strings.Contains(low, w) && !strings.HasSuffix(low, "_env") && !strings.HasSuffix(low, "_file") {
						if _, ok := notSecrets[p]; !ok {
							t.Errorf("%s looks like it holds a secret value; make it a reference (_env, _file or a secret-store reference) or list it in notSecrets with the reason", p)
						}
					}
				}
				walk(f.Type, p, depth+1)
			}
		}
		walk(reflect.TypeOf(k.spec()), k.Name, 0)
	}
	for p := range freeMaps {
		if !seenMaps[p] {
			t.Errorf("freeMaps lists %s, which no kind has any more", p)
		}
	}
}
