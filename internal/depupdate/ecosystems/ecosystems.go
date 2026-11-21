// Package ecosystems lists the parsers of every ecosystem that works today.
package ecosystems

import (
	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/container"
	"github.com/jk-magic-creations/launchway/internal/depupdate/gomod"
	"github.com/jk-magic-creations/launchway/internal/depupdate/npm"
	"github.com/jk-magic-creations/launchway/internal/depupdate/python"
	"github.com/jk-magic-creations/launchway/internal/depupdate/workflow"
)

// Parsers returns one parser per available ecosystem. imageGlobs are extra
// files whose NAME_IMAGE='ref' lines are image references.
func Parsers(imageGlobs []string) []depupdate.Parser {
	return []depupdate.Parser{gomod.Parser{}, npm.Parser{}, python.Parser{}, container.Parser{Extra: imageGlobs}, workflow.Parser{}}
}
