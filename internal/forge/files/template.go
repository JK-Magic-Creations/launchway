package files

import (
	"context"
	"errors"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// PullTemplatePaths are the files a pull request's description is taken from, in the order they are looked for:
// the one Launchway names first, then the two conventional top-level names.
var PullTemplatePaths = []string{".launchway/pull_request_template.md", "pull_request_template.md", "PULL_REQUEST_TEMPLATE.md"}

// MaxTemplateBytes is the most of a template that is used; a larger file is no template.
const MaxTemplateBytes = 64 << 10

// PullTemplate is the description to start a pull request into ref with.
type PullTemplate struct {
	// Path is the file it came from, empty when the repository has none.
	Path     string `json:"path"`
	Template string `json:"template"`
	// Commit is the commit it was read at.
	Commit string `json:"commit,omitempty"`
}

// PullTemplate reads the template of a repository at a ref (the pull request's target branch): the first of
// PullTemplatePaths that is a text file within MaxTemplateBytes. A repository with none, or an empty one, has an
// empty template, not an error.
func (s *Service) PullTemplate(ctx context.Context, repoID, ref string) (PullTemplate, error) {
	for _, p := range PullTemplatePaths {
		it, err := s.Read(ctx, repoID, ref, p)
		switch {
		case err == nil && it.Type == "file" && !it.Truncated && it.Encoding == "utf8" && int64(len(it.Content)) <= MaxTemplateBytes:
			return PullTemplate{Path: p, Template: normaliseTemplate(it.Content), Commit: it.Commit}, nil
		case err == nil, errors.Is(err, forge.ErrNotFound):
			continue // not there, or not a text file of a size to start from
		default:
			return PullTemplate{}, err
		}
	}
	return PullTemplate{}, nil
}

// normalise is a template as it is offered: line ends of the file's own kind become newlines.
func normaliseTemplate(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
