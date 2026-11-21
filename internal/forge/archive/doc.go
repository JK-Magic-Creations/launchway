// Package archive keeps another server's issues and their comments, as a
// read-only record attached to a hosted repository, so that history written
// before a move stays readable and searchable in Launchway. It is not an issue
// tracker: nothing here is created, edited or closed by a person; an import
// stores what the source's export holds, and the same number imported again
// replaces what was stored for it.
//
// The document shape an import reads is described in docs/reference/archive-format.md.
package archive
