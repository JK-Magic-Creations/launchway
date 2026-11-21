package main

// Linking the storage package is what makes the binary answer git's hooks of
// hosted repositories: they are symlinks to it, and the package registers the
// entry that handles them (see gitexec.RegisterEntry, which main runs first).
import _ "github.com/jk-magic-creations/launchway/internal/forge/storage"
