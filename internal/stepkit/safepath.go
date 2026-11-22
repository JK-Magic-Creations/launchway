package stepkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafeJoin returns root joined with the relative path rel, only if every
// part of it that exists is a real directory or file and not a link, so that a
// path a step lists as writable cannot lead out of the workspace through a
// link. The check is made part by part, as the library unpacking does (see
// noLinkAbove in the runner). A part that does not exist yet is fine: the
// caller makes it. A race between this check and its use is closed by the
// caller opening what it got with os.Root, or mounting it read-write only
// after this check and with the workspace itself held read-only.
func SafeJoin(root, rel string) (string, error) {
	if msg := checkRel(rel); msg != "" {
		return "", errors.New(msg)
	}
	if rel == "." {
		return root, nil
	}
	cur := root
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Join(root, filepath.FromSlash(rel)), nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%q is a link, and a step cannot be given the place it leads to", rel)
		}
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}
