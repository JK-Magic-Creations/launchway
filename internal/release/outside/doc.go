// Package outside publishes a Launchway release on an outside Git host: the
// host a push mirror copies a repository to. The release page there (its
// notes and its files) is made after the tag has reached it. A host is one
// implementation of Publisher; nothing outside this package names one. See
// docs/development/release-publish-outside.md.
package outside
