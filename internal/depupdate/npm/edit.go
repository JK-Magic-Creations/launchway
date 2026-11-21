package npm

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

// Command is what runs in the package's directory, inside the pinned Node
// image, to bring package-lock.json with an edited package.json.
type Command struct {
	Dir  string
	Args [][]string
}

// LockCommands regenerates only the lock file from package.json, without
// running any package's install scripts: `npm install --package-lock-only
// --ignore-scripts`.
func LockCommands(dir string) Command {
	return Command{Dir: dir, Args: [][]string{{"npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund"}}}
}

// EditManifest rewrites one dependency's range in package.json text, keeping
// the file's formatting and the range's operator. It returns the new text.
// A range that cannot be rewritten (a union such as ">=1 <2") is an error:
// the pull request then asks a person to choose the range.
func EditManifest(src, name, to string) (string, error) {
	re := regexp.MustCompile(`(["]` + regexp.QuoteMeta(name) + `"\s*:\s*")([^"]*)(")`)
	m := re.FindStringSubmatchIndex(src)
	if m == nil {
		return src, fmt.Errorf("%s is not in package.json", name)
	}
	old := src[m[4]:m[5]]
	nu, ok := version.Rewrite(old, to)
	if !ok {
		return src, fmt.Errorf("the range %q of %s cannot be rewritten automatically", old, name)
	}
	return src[:m[4]] + nu + src[m[5]:], nil
}

// Section reports which dependency map of package.json holds a name; the
// edit above is by key, which is unique within a manifest in practice.
func Section(d depupdate.Dep) string {
	switch d.Group {
	case depupdate.GroupDev:
		return "devDependencies"
	case depupdate.GroupOpt:
		return "optionalDependencies"
	}
	return strings.TrimSpace("dependencies")
}
