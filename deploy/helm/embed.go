// Package helm carries the Launchway chart inside the program, so `launchway install` installs
// exactly the chart this build was made with and needs no Helm, no network and no chart
// repository. The chart on disk (deploy/helm/launchway) is the one source.
package helm

import "embed"

// Chart is the Launchway chart's files, under the directory "launchway".
//
//go:embed all:launchway
var Chart embed.FS
