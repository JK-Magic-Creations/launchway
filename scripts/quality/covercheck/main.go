// Command covercheck compares per-package coverage with the checked-in floor.
//
//	covercheck check  coverage.txt quality/coverage-floor.json
//	covercheck update coverage.txt quality/coverage-floor.json
//
// coverage.txt is the output of `go test -cover ./...`. The floor holds each
// package's coverage rounded down to 0.1. check fails when a package is below
// its floor, or when a package without a floor starts below 70%. update raises
// floors (and adds new packages); it never lowers one.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	modulePrefix  = "github.com/jk-magic-creations/launchway/"
	newPackageMin = 70.0
)

type floorFile struct {
	Comment  string             `json:"comment"`
	Packages map[string]float64 `json:"packages"`
}

var (
	covRe  = regexp.MustCompile(`coverage: ([0-9.]+)% of statements`)
	noTest = regexp.MustCompile(`\[no test files\]`)
)

func main() {
	if len(os.Args) != 4 || (os.Args[1] != "check" && os.Args[1] != "update") {
		fmt.Fprintln(os.Stderr, "usage: covercheck check|update coverage.txt coverage-floor.json")
		os.Exit(2)
	}
	mode, covPath, floorPath := os.Args[1], os.Args[2], os.Args[3]
	current := parse(covPath)
	floors := floorFile{Packages: map[string]float64{}}
	if b, err := os.ReadFile(floorPath); err == nil {
		if err := json.Unmarshal(b, &floors); err != nil {
			fatal("%s: %v", floorPath, err)
		}
	}

	names := make([]string, 0, len(current))
	for n := range current {
		names = append(names, n)
	}
	sort.Strings(names)

	if mode == "update" {
		for _, n := range names {
			f := roundDown(current[n])
			if f > floors.Packages[n] || !has(floors.Packages, n) {
				floors.Packages[n] = f
			}
		}
		floors.Comment = "Per-package statement coverage floors (rounded down to 0.1). They only move up: `make coverage-update`."
		out, _ := json.MarshalIndent(floors, "", "  ")
		if err := os.WriteFile(floorPath, append(out, '\n'), 0o644); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("coverage floors written for %d packages\n", len(floors.Packages))
		return
	}

	failed := 0
	raised := 0
	for _, n := range names {
		got := roundDown(current[n])
		floor, ok := floors.Packages[n]
		switch {
		case !ok && got < newPackageMin:
			fmt.Fprintf(os.Stderr, "coverage: new package %s has %.1f%%, a new package needs at least %.0f%%\n", n, got, newPackageMin)
			failed++
		case ok && got < floor:
			fmt.Fprintf(os.Stderr, "coverage: %s dropped to %.1f%% (floor %.1f%%). Add tests for the new code\n", n, got, floor)
			failed++
		case ok && got > floor:
			raised++
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "coverage: %d package(s) below the floor\n", failed)
		os.Exit(1)
	}
	fmt.Printf("coverage: ok (%d packages, none below its floor)\n", len(names))
	if raised > 0 {
		fmt.Printf("coverage: %d package(s) are above their floor; run `make coverage-update` to raise it\n", raised)
	}
}

func has(m map[string]float64, k string) bool { _, ok := m[k]; return ok }

func roundDown(v float64) float64 { return math.Floor(v*10+1e-9) / 10 }

func parse(path string) map[string]float64 {
	f, err := os.Open(path)
	if err != nil {
		fatal("%v", err)
	}
	defer f.Close()
	out := map[string]float64{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		var pkg string
		switch fields[0] {
		case "ok", "FAIL", "?":
			pkg = fields[1]
		default:
			if !strings.HasPrefix(fields[0], "coverage:") && fields[0] != "---" {
				continue
			}
			continue
		}
		if fields[0] == "FAIL" && !covRe.MatchString(sc.Text()) {
			fatal("tests failed in %s; coverage cannot be measured: %s", pkg, sc.Text())
		}
		pkg = strings.TrimPrefix(pkg, modulePrefix)
		if noTest.MatchString(sc.Text()) {
			out[pkg] = 0
			continue
		}
		if m := covRe.FindStringSubmatch(sc.Text()); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			out[pkg] = v
		}
	}
	return out
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
