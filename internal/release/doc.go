// Package release holds the rules of releases that need no database and no
// Git: versions and their order, the next version proposed from a list of
// changes, release notes grouped and written as Markdown, change windows and
// freezes, and the four delivery measures. The engine stores releases; the
// server and the source package read Git; this package decides.
package release
