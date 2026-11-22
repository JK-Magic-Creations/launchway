package inspect

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/yaml"
)

// ErrNotInRelease is returned for an object the release does not own.
var ErrNotInRelease = errors.New("that object does not belong to this release")

// Find returns a node of the tree.
func (t *Tree) Find(id string) (*Node, bool) {
	for i := range t.Nodes {
		if t.Nodes[i].ID == id {
			return &t.Nodes[i], true
		}
	}
	return nil, false
}

// Manifest returns the live object as redacted YAML. The object must be in
// the release's tree: nothing else in the namespace can be read through it.
func (b *Builder) Manifest(ctx context.Context, t *Tree, id string) (string, error) {
	n, ok := t.Find(id)
	if !ok {
		return "", ErrNotInRelease
	}
	if _, known := kinds[n.Kind]; !known {
		return "", fmt.Errorf("the inspector does not read objects of kind %s", n.Kind)
	}
	obj, err := b.R.Get(ctx, n.Kind, n.Namespace, n.Name)
	if err != nil {
		return "", err
	}
	if obj == nil {
		return "", fmt.Errorf("%s no longer exists", id)
	}
	if obj["kind"] == nil {
		obj["kind"] = n.Kind
	}
	red := t.red
	if red == nil {
		red = b.Red
	}
	out, err := yaml.Marshal(red.Manifest(obj))
	if err != nil {
		return "", err
	}
	return string(out), nil
}
