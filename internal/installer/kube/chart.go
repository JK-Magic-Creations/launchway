// Package kube is the Kubernetes target of `launchway install`: it reads the cluster, checks what
// Launchway needs, writes the small values file, renders the embedded chart and applies it, and
// translates what goes wrong into plain words. It carries Helm's chart reader and template engine
// and the Kubernetes client, so no helm or kubectl is needed on the machine.
package kube

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"

	"github.com/jk-magic-creations/launchway/deploy/helm"
)

// LoadChart reads the embedded chart.
func LoadChart() (*chart.Chart, error) {
	var files []*loader.BufferedFile
	err := fs.WalkDir(helm.Chart, "launchway", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(helm.Chart, p)
		if err != nil {
			return err
		}
		files = append(files, &loader.BufferedFile{Name: strings.TrimPrefix(p, "launchway/"), Data: b})
		return nil
	})
	if err != nil {
		return nil, err
	}
	c, err := loader.LoadFiles(files)
	if err != nil {
		return nil, fmt.Errorf("the chart inside this program: %w", err)
	}
	return c, nil
}

// ChartFile returns one file of the embedded chart (the examples, the defaults).
func ChartFile(name string) ([]byte, error) {
	return fs.ReadFile(helm.Chart, path.Join("launchway", name))
}
