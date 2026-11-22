package stepkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// firstLink walks the parts below root and says whether one that exists is a link. A part that does not
// exist ends the walk: nothing deeper exists either. It is the one check of "no link on the way", used by
// SafeJoin and by NoLinkAbove (the unpacking of a library in the runner).
func firstLink(root string, parts []string) (bool, error) {
	cur := root
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

// NoLinkAbove reports an error if a folder above name (relative, local) inside dest is a link rather than a
// real directory, so that nothing is created through a link an earlier entry made.
func NoLinkAbove(dest, name string) error {
	dir := filepath.Dir(name)
	if dir == "." {
		return nil
	}
	link, err := firstLink(dest, strings.Split(dir, string(filepath.Separator)))
	if err != nil {
		return err
	}
	if link {
		return errors.New("that folder is a link")
	}
	return nil
}

// SafeJoin returns root joined with the relative path rel, only if every
// part of it that exists is a real directory or file and not a link, so that a
// path a step lists as writable cannot lead out of the workspace through a
// link. The check is made part by part, as the library unpacking does (the
// same walk: NoLinkAbove). A part that does not exist yet is fine: the
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
	link, err := firstLink(root, strings.Split(rel, "/"))
	if err != nil {
		return "", err
	}
	if link {
		return "", fmt.Errorf("%q is a link, and a step cannot be given the place it leads to", rel)
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}
