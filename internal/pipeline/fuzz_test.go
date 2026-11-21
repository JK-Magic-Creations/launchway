package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// Fuzz targets for the parsers that read text anyone with push access controls. The property is
// the same everywhere: any input, however broken, gives a result or an error, never a panic, a
// hang or a result that is nil without an error. `make security` runs each for a short time; a
// failing input is saved under testdata/fuzz and then runs with every `go test`.

func seedYAML(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "examples", "*.yaml"))
	more, _ := filepath.Glob(filepath.Join("..", "..", ".launchway", "*.yaml"))
	for _, name := range append(files, more...) {
		if b, err := os.ReadFile(name); err == nil {
			f.Add(b)
		}
	}
	for _, s := range []string{
		"",
		"pipeline: x\nsteps:\n  a: { run: echo hi }\n",
		"pipeline: x\nsteps:\n  a: { run: x, needs: a }\n",
		"pipeline: x\nsteps:\n  a: { uses: lib/t, with: { k: v } }\n",
		"&a [*a]",
		"a: &a\n  b: *a\n",
		"pipeline: x\nsteps: {" + "a: {run: x}, b: {run: y, needs: [a]}" + "}\n",
		"\x00\xff",
	} {
		f.Add([]byte(s))
	}
}

func FuzzParse(f *testing.F) {
	seedYAML(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data)
		if err == nil && p == nil {
			t.Fatal("Parse returned neither a pipeline nor an error")
		}
	})
}

func FuzzParseCron(f *testing.F) {
	for _, s := range []string{"", "* * * * *", "*/5 0-23/2 1,15 */3 mon-fri", "@daily", "60 24 32 13 8", "0 0 31 2 *", "-1 * * * *", "1-2-3 * * * *"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		c, err := ParseCron(expr)
		if err == nil && c == nil {
			t.Fatal("ParseCron returned neither a schedule nor an error")
		}
	})
}

// FuzzDescribeTemplate reads a library template, which comes from another repository.
func FuzzDescribeTemplate(f *testing.F) {
	seedYAML(f)
	f.Add([]byte("params:\n  a: { type: string, secret: true }\nsteps:\n  s: { run: echo ${{ params.a }} }\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = DescribeTemplate(data, "fuzz")
	})
}
