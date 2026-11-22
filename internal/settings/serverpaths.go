package settings

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Settings that name a file on the server are the operator's, not an
// API administrator's. A role binding that gives the admin role on every
// project is not a login on the server host, yet a token_file or a ca_file
// the server then reads would turn it into one (any file the server's
// account can read, its own service-account token among them). So these keys
// are set in the bootstrap file, which is how the operator is already
// trusted; the first start's import brings them in, and the API keeps
// what is there but refuses to set or change one.

// pathKeys are the settings that name a file; at is where, "" for anywhere.
var pathKeys = []struct{ key, at string }{
	{"token_file", ""}, {"secret_file", ""}, {"hmac_secret_file", ""}, {"ca_file", ""}, {"jwt_file", ""},
	{"client_secret_file", ""}, {"dir", "artifacts"},
}

// serverPaths returns the file-naming settings of a spec, by their path.
func serverPaths(kind string, spec json.RawMessage) map[string]string {
	var g any
	if json.Unmarshal(spec, &g) != nil {
		return nil
	}
	out := map[string]string{}
	var walk func(v any, path []string)
	walk = func(v any, path []string) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				if s, ok := x.(string); ok && s != "" {
					for _, pk := range pathKeys {
						if k == pk.key && (pk.at == "" || (len(path) > 0 && path[len(path)-1] == pk.at)) {
							out[strings.Join(append(append([]string{}, path...), k), ".")] = s
						}
					}
				}
				walk(x, append(append([]string{}, path...), k))
			}
		case []any:
			for i, x := range t {
				walk(x, append(append([]string{}, path...), fmt.Sprint(i)))
			}
		}
	}
	walk(g, nil)
	return out
}

// checkServerPaths refuses a change that sets or changes a setting naming a
// file on the server. A setting left as it was is fine.
func checkServerPaths(have []engine.ConfigObject, puts []engine.ConfigObject) error {
	cur := map[string]engine.ConfigObject{}
	for _, o := range have {
		if !o.Deleted() {
			cur[o.Kind+"\x00"+o.Name] = o
		}
	}
	var bad []string
	for _, p := range puts {
		before := serverPaths(p.Kind, cur[p.Kind+"\x00"+p.Name].Spec)
		for path, v := range serverPaths(p.Kind, p.Spec) {
			if before[path] != v {
				bad = append(bad, fmt.Sprintf("%s %s: %s names a file on the server, so it can only be set in the bootstrap file, not through the API (an administrator of Flights is not an operator of the server, and could point it at any file the server can read)", p.Kind, p.Name, path))
			}
		}
	}
	sort.Strings(bad)
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(bad, "\n"))
}
