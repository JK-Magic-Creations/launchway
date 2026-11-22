package inspect

import (
	"regexp"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// Mask is what replaces a hidden value.
const Mask = "***"

var (
	// secretName matches names of things that hold secrets.
	secretName = regexp.MustCompile(`(?i)(pass(word|wd)?|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|authorization|bearer|dsn|connection[_-]?string|cookie|signing|salt|cert(ificate)?[_-]?key)`)
	// assignment is name=value or name: value in text.
	assignment = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:pass(?:word|wd)?|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|authorization|client[_-]?secret)[A-Za-z0-9_.-]*)(["']?\s*[:=]\s*["']?)([^\s"',;&}\]]+)`)
	bearer     = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	userinfo   = regexp.MustCompile(`(://)[^/\s:@]+:[^/\s@]+@`)
	jwt        = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	tokens     = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{30,})\b`)
	pem        = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
)

// Redactor hides secrets in text and manifests: values it was told (the
// values of the namespace's Secrets, which it reads but never sends), and
// anything shaped like a credential.
type Redactor struct {
	known  *secrets.Masker
	values []string
}

// NewRedactor makes one that also hides the given values.
func NewRedactor(known ...string) *Redactor {
	m := map[string]string{}
	for i, k := range known {
		m[string(rune('a'+i%26))+strings.Repeat("_", i/26)] = k
	}
	km, _ := secrets.NewMasker(m)
	return &Redactor{known: km, values: known}
}

// Text redacts a piece of text, such as a log line.
func (r *Redactor) Text(s string) string {
	if r != nil {
		s = r.known.Mask(s)
	}
	if s == "" {
		return s
	}
	s = pem.ReplaceAllString(s, "-----BEGIN PRIVATE KEY----- "+Mask)
	s = jwt.ReplaceAllString(s, Mask)
	s = tokens.ReplaceAllString(s, Mask)
	s = userinfo.ReplaceAllString(s, "${1}"+Mask+":"+Mask+"@")
	s = bearer.ReplaceAllString(s, "${1} "+Mask)
	s = assignment.ReplaceAllString(s, "${1}${2}"+Mask)
	return s
}

// Value redacts a value known by its name: a secret-looking name hides it
// whole.
func (r *Redactor) Value(name, value string) string {
	if secretName.MatchString(name) && value != "" {
		return Mask
	}
	return r.Text(value)
}

// Data redacts a ConfigMap's data and clips each value to limit bytes.
func (r *Redactor) Data(in map[string]string, limit int) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		v = r.Value(k, v)
		if limit > 0 && len(v) > limit {
			v = v[:limit] + "\n… (clipped, " + itoa(len(v)-limit) + " more bytes)"
		}
		out[k] = v
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Manifest returns a redacted copy of an object, fit to show: managed
// fields and the last-applied annotation are dropped (the latter holds a
// whole copy of the object), a Secret's values are replaced by their sizes,
// container env values with a secret-looking name are hidden, and every
// string passes Text.
func (r *Redactor) Manifest(obj map[string]any) map[string]any {
	kind, _ := obj["kind"].(string)
	out, _ := r.walk("", obj).(map[string]any)
	if meta, ok := out["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
		if an, ok := meta["annotations"].(map[string]any); ok {
			delete(an, "kubectl.kubernetes.io/last-applied-configuration")
			if len(an) == 0 {
				delete(meta, "annotations")
			}
		}
	}
	if kind == "Secret" {
		for _, f := range []string{"data", "stringData"} {
			if m, ok := obj[f].(map[string]any); ok {
				hidden := map[string]any{}
				for k, v := range m {
					s, _ := v.(string)
					hidden[k] = "<hidden: " + itoa(len(s)) + " bytes>"
				}
				out[f] = hidden
			}
		}
	}
	return out
}

func (r *Redactor) walk(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		// An env entry {name: X, value: Y}: the name says what Y is.
		if n, ok := t["name"].(string); ok {
			if val, ok := t["value"].(string); ok {
				for k, x := range t {
					out[k] = r.walk(k, x)
				}
				out["value"] = r.Value(n, val)
				return out
			}
		}
		for k, x := range t {
			out[k] = r.walk(k, x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = r.walk(key, x)
		}
		return out
	case string:
		if strings.HasSuffix(strings.ToLower(key), "name") {
			return r.Text(t) // secretName: db-credentials names a Secret, it is not one
		}
		return r.Value(key, t)
	}
	return v
}

// With returns a redactor that hides the given values too (and what r
// hides). The values are not kept beyond it.
func (r *Redactor) With(values ...string) *Redactor {
	if r == nil {
		return NewRedactor(values...)
	}
	all := append(slices.Clone(r.values), values...)
	nr := NewRedactor(all...)
	return nr
}
