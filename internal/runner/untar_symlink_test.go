package runner

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func tarOf(t *testing.T, hs []tar.Header, bodies map[string]string) *bytes.Reader {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for i := range hs {
		h := hs[i]
		body := bodies[h.Name]
		h.Size = int64(len(body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b.Bytes())
}

// A library archive must not be able to place a file outside its directory:
// neither by a link that points out and a file written through it, nor by a
// link that points out at all.
func TestUntarRefusesSymlinkEscapes(t *testing.T) {
	cases := map[string][]tar.Header{
		"write through an absolute link": {
			{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "OUTSIDE", Mode: 0o777},
			{Name: "l/pwned", Typeflag: tar.TypeReg, Mode: 0o644},
		},
		"relative link out": {
			{Name: "d/l", Typeflag: tar.TypeSymlink, Linkname: "../../x", Mode: 0o777},
		},
		"link to self then through it": {
			{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "a/b", Typeflag: tar.TypeSymlink, Linkname: "../x", Mode: 0o777},
		},
	}
	for name, hs := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "dest")
			outside := filepath.Join(root, "outside")
			if err := os.MkdirAll(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			hs := append([]tar.Header(nil), hs...)
			for i := range hs {
				if hs[i].Linkname == "OUTSIDE" {
					hs[i].Linkname = outside
				}
			}
			err := untar(tarOf(t, hs, map[string]string{"l/pwned": "x"}), dest)
			if err == nil {
				t.Fatal("untar accepted an archive that escapes its directory")
			}
			if _, serr := os.Stat(filepath.Join(outside, "pwned")); serr == nil {
				t.Fatal("a file was written outside the directory")
			}
		})
	}
}

func TestUntarKeepsInsideLinks(t *testing.T) {
	dest := t.TempDir()
	hs := []tar.Header{
		{Name: "bin/run.sh", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "bin/alias", Typeflag: tar.TypeSymlink, Linkname: "run.sh", Mode: 0o777},
		{Name: "top", Typeflag: tar.TypeSymlink, Linkname: "bin/run.sh", Mode: 0o777},
	}
	if err := untar(tarOf(t, hs, map[string]string{"bin/run.sh": "#!/bin/sh\n"}), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "top")); err != nil {
		t.Fatal(err)
	}
}
