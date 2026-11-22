package main

// launchway import pipeline: read the pipeline files another system left in a
// repository and propose a Launchway pipeline, with a finding for everything it
// converted with a difference or could not convert. It runs on this machine, with
// no server, and never touches the old files.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/importer"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

const importUsage = `Usage: launchway import pipeline [--from FORMAT|auto] [--dir .] [--write] [--report FILE] [--dry] [--file PATH]

Reads the pipeline files another system left in the repository and proposes a Launchway pipeline.
Without --write it prints the proposal and the findings: every construct is listed as converted exactly,
converted with a difference, or not converted (with the reason and what to do). --write saves the
first proposal as .launchway/pipeline.yaml (more files go to .launchway/imported/ as proposals that
are not active) and never touches the old files. A proposal that does not pass launchway validate is
not written.
`

func importHelpText() string {
	var b strings.Builder
	b.WriteString(importUsage)
	b.WriteString("\nFormats (--from):\n")
	for _, f := range importer.Formats() {
		fmt.Fprintf(&b, "  %-20s %s: %s\n", f.ID, f.Title, f.Where)
	}
	b.WriteString(`  auto                 find every file of a known format under --dir (the default)

Options:
  --dir DIR       the repository to read (default .)
  --file PATH     import only this file (a path relative to --dir)
  --write         save the proposal; --force replaces an existing .launchway/pipeline.yaml
  --report FILE   save the findings as a report (Markdown, or JSON when the name ends in .json)
  --dry           switch off every step that pushes, releases or launches until the input enable_delivery is true,
                  so the new pipeline can run beside the old one and results can be compared first
  --json          print the whole result as JSON
  --verbose       list the constructs that converted exactly too
  --name NAME     the pipeline's name (default from the file)
  --no-templates  write explicit steps even where a built-in template fits
`)
	return b.String()
}

func cmdImport(args []string) error {
	if len(args) == 0 || args[0] != "pipeline" {
		fmt.Print(importUsage)
		return cliErr{exitUsage, ""}
	}
	fl := flag.NewFlagSet("import pipeline", flag.ContinueOnError)
	from := fl.String("from", "auto", "the format of the files, or auto")
	dir := fl.String("dir", ".", "the repository to read")
	file := fl.String("file", "", "import only this file")
	write := fl.Bool("write", false, "save the proposal")
	force := fl.Bool("force", false, "replace an existing pipeline file")
	report := fl.String("report", "", "save the findings to this file")
	dry := fl.Bool("dry", false, "switch off steps that deliver")
	asJSON := fl.Bool("json", false, "print JSON")
	verbose := fl.Bool("verbose", false, "list exact conversions too")
	name := fl.String("name", "", "the pipeline's name")
	noTpl := fl.Bool("no-templates", false, "write explicit steps")
	if _, err := parseArgs(fl, args[1:]); err != nil {
		return err
	}
	files, err := collectImportFiles(*dir)
	if err != nil {
		return err
	}
	res, err := importer.Import(importer.Request{Format: *from, Files: files, Root: filepath.ToSlash(*file), Options: importer.Options{Name: *name, Dry: *dry, NoTemplates: *noTpl}})
	if errors.Is(err, importer.ErrNothing) {
		fmt.Printf("No pipeline files of a known format were found under %s.\n", *dir)
		fmt.Println("Known formats:")
		for _, f := range importer.Formats() {
			fmt.Printf("  %-20s %s: %s\n", f.ID, f.Title, f.Where)
		}
		fmt.Println("Name the format and the file when the file has another name: launchway import pipeline --from FORMAT --file PATH")
		return cliErr{1, ""}
	}
	if err != nil {
		return err
	}
	if *report != "" {
		if err := writeImportReport(*report, res); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Report saved to %s\n", *report)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	} else {
		printImport(res, *verbose, *write)
	}
	if !*write {
		if !*asJSON {
			fmt.Println("\nThis is only printed. Save it with: launchway import pipeline --write")
		}
		return nil
	}
	return writeImported(res, *dir, *force)
}

// collectImportFiles reads the files under dir that a reader might want: YAML and
// Groovy files and files named ...file, up to the importer's limits.
func collectImportFiles(dir string) ([]importer.File, error) {
	var out []importer.File
	total := 0
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil // an unreadable directory yields no files, as the walk did
	}
	defer root.Close()
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".venv", "target", "dist", "build", "imported":
				if rel != "." {
					return filepath.SkipDir
				}
			}
			if strings.Count(rel, string(filepath.Separator)) >= 4 {
				return filepath.SkipDir
			}
			return nil
		}
		lower := strings.ToLower(d.Name())
		if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".groovy") && !strings.HasSuffix(lower, "file") {
			return nil
		}
		if rel == filepath.Join(".launchway", "pipeline.yaml") || rel == filepath.Join(".launchway", "pipeline.yml") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > int64(importer.DefaultLimits.MaxBytes) || len(out) >= importer.MaxFiles || total+int(info.Size()) > importer.MaxTotalBytes {
			return nil
		}
		b, err := root.ReadFile(rel)
		if err != nil {
			return nil
		}
		total += len(b)
		out = append(out, importer.File{Path: filepath.ToSlash(rel), Content: b})
		return nil
	})
	return out, err
}

var classWords = map[importer.Class]string{importer.Exact: "converted exactly", importer.Different: "converted with a difference", importer.Manual: "needs a person"}

func printImport(res *importer.Result, verbose, written bool) {
	for _, p := range res.Plans {
		fmt.Printf("== %s (%s)\n\n", strings.Join(p.Files, ", "), formatTitle(p.Format))
		if p.Pipeline != "" && !written {
			fmt.Println(p.Pipeline)
		}
		n := importer.Count(p.Findings)
		fmt.Printf("Findings: %d converted exactly, %d converted with a difference, %d need a person.\n", n[importer.Exact], n[importer.Different], n[importer.Manual])
		for _, c := range []importer.Class{importer.Manual, importer.Different, importer.Exact} {
			if c == importer.Exact && !verbose {
				continue
			}
			first := true
			for _, f := range p.Findings {
				if f.Class != c {
					continue
				}
				if first {
					fmt.Printf("\n%s:\n", strings.ToUpper(classWords[c][:1])+classWords[c][1:])
					first = false
				}
				loc := f.File
				if f.Line > 0 {
					loc = fmt.Sprintf("%s:%d", f.File, f.Line)
				}
				fmt.Printf("  %s  %s\n", loc, f.Message)
				if f.Next != "" {
					fmt.Printf("      Next: %s\n", f.Next)
				}
			}
		}
		if len(p.Secrets) > 0 {
			fmt.Printf("\nSecrets to create (names only; no value was read): %s\n", strings.Join(p.Secrets, ", "))
		}
		fmt.Println()
	}
}

func formatTitle(id string) string {
	if f := importer.FormatByID(id); f != nil {
		return f.Title
	}
	return id
}

func writeImported(res *importer.Result, root string, force bool) error {
	wrote := 0
	for _, p := range res.Plans {
		if p.Pipeline == "" {
			fmt.Fprintf(os.Stderr, "%s: nothing was written: the file could not be read as a pipeline (see the findings)\n", strings.Join(p.Files, ", "))
			continue
		}
		if _, err := source.ExpandOffline([]byte(p.Pipeline), nil, pipeline.BindOptions{}, pipeline.WithProjectFacts(nil, pipeline.ProjectFacts{Project: p.Name})); err != nil {
			var ps pipeline.Problems
			if errors.As(err, &ps) {
				for _, pr := range ps {
					fmt.Fprintf(os.Stderr, "%s: proposal line %d: %s\n", strings.Join(p.Files, ", "), pr.Line, pr.Message)
				}
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
			fmt.Fprintln(os.Stderr, "Nothing was written for this file: the proposal does not pass launchway validate.")
			continue
		}
		dest := filepath.Join(root, source.PipelinePaths[0])
		if wrote > 0 {
			dest = filepath.Join(root, ".launchway", "imported", strings.TrimSuffix(filepath.Base(p.Files[0]), filepath.Ext(p.Files[0]))+".yaml")
		}
		if _, err := os.Stat(dest); err == nil && !force {
			fmt.Printf("%s already exists, so it was left alone. Use --force to replace it.\n", dest)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(p.Pipeline), 0o600); err != nil {
			return err
		}
		wrote++
		if strings.Contains(dest, "imported") {
			fmt.Printf("Wrote %s (a proposal; it does not run until you move it to .launchway/pipeline.yaml).\n", dest)
		} else {
			fmt.Printf("Wrote %s. The old files were not touched. Check it with: launchway validate --resolved\n", dest)
		}
	}
	if wrote == 0 {
		return cliErr{1, ""}
	}
	return nil
}

func writeImportReport(path string, res *importer.Result) error {
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		b, _ := json.MarshalIndent(res, "", "  ")
		return os.WriteFile(path, b, 0o600)
	}
	var b strings.Builder
	b.WriteString("# Import report\n\n")
	for _, p := range res.Plans {
		n := importer.Count(p.Findings)
		fmt.Fprintf(&b, "## %s (%s)\n\n%d converted exactly, %d converted with a difference, %d need a person.\n\n", strings.Join(p.Files, ", "), formatTitle(p.Format), n[importer.Exact], n[importer.Different], n[importer.Manual])
		b.WriteString("| Where | What happened | Next |\n|---|---|---|\n")
		for _, c := range []importer.Class{importer.Manual, importer.Different, importer.Exact} {
			for _, f := range p.Findings {
				if f.Class == c {
					fmt.Fprintf(&b, "| %s:%d | **%s.** %s | %s |\n", f.File, f.Line, classWords[c], strings.ReplaceAll(f.Message, "|", "/"), strings.ReplaceAll(f.Next, "|", "/"))
				}
			}
		}
		if len(p.Secrets) > 0 {
			fmt.Fprintf(&b, "\nSecrets to create (names only): %s\n", strings.Join(p.Secrets, ", "))
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
