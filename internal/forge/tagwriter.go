package forge

import "context"

// TagWriter makes annotated tags in the repositories a server stores itself.
// A provider that keeps repositories in this installation has no host to ask:
// the server writes the tag, under the same rules as a push, and tells the
// mirrors and triggers as a push would. The server gives the built-in
// provider its writer (SetTags), and the Releases group of the provider calls
// it, so a release is cut the same way whether the repository lives here or on
// an outside host.
type TagWriter interface {
	// CreateTag makes the annotated tag and returns the tag object's id.
	CreateTag(ctx context.Context, repo Repository, t TagSpec) (string, error)
}

// TagSetter is a provider that can be given a TagWriter.
type TagSetter interface{ SetTags(TagWriter) }
