package coverage

import (
	"bytes"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
)

// maxTextLine is the longest LCOV or coverprofile line that is read;
// longer lines are skipped with a warning.
const maxTextLine = 1 << 20

// lineScanner hands out the lines of a text file, skipping and counting
// those longer than maxTextLine.
type lineScanner struct {
	data   []byte
	n      int // number of the line last returned
	long   int // lines skipped for their length
	longAt int
}

// next returns the next line without its line ending, and whether it was
// too long to read (then it holds only the line's start, for its prefix).
func (s *lineScanner) next() (line []byte, tooLong, ok bool) {
	if len(s.data) == 0 {
		return nil, false, false
	}
	s.n++
	i := bytes.IndexByte(s.data, '\n')
	if i < 0 {
		line, s.data = s.data, nil
	} else {
		line, s.data = s.data[:i], s.data[i+1:]
	}
	if len(line) > maxTextLine {
		s.long++
		if s.longAt == 0 {
			s.longAt = s.n
		}
		return line[:16], true, true
	}
	return bytes.TrimSpace(line), false, true
}

func (s *lineScanner) warnings(c *collector, bad, badAt int) {
	if s.long > 0 {
		c.warn("%d lines longer than 1 MiB were skipped (the first is line %d)", s.long, s.longAt)
	}
	if bad > 0 {
		c.warn("%d lines could not be read (the first is line %d)", bad, badAt)
	}
}

// parseLCOV reads an LCOV tracefile. Lines come from DA records, branches
// from BRDA records (each one branch; "-" means not taken) and functions
// from FN, FNDA and FNA records, joined by function name. A record's own
// LF/LH, BRF/BRH and FNF/FNH are used only for a kind it has no such
// records for. Several records for the same SF are merged: a line, branch
// or function is covered when any record says so.
func parseLCOV(dl *deadline, data []byte, lim Limits) (*Report, error) {
	c := newCollector(FormatLCOV, lim)
	s := &lineScanner{data: data}
	var cur *fileAcc
	var rec Totals // the record's own LF/LH and friends, not yet checked
	bad, badAt := 0, 0
	flush := func() {
		if cur != nil {
			cur.fb = cur.fb.add(Totals{Lines: rec.Lines.norm(), Branches: rec.Branches.norm(), Functions: rec.Functions.norm()})
		}
		rec = Totals{}
	}
	fail := func() {
		bad++
		if badAt == 0 {
			badAt = s.n
		}
	}
	for {
		if dl.tick() {
			if !c.any() {
				return nil, dl.stopError()
			}
			c.warn("parsing %s; coverage after line %d is missing", dl.what(), s.n)
			break
		}
		line, tooLong, ok := s.next()
		if !ok {
			break
		}
		if tooLong {
			if bytes.HasPrefix(line, []byte("SF:")) {
				flush()
				cur = nil
			}
			continue
		}
		if len(line) == 0 {
			continue
		}
		key, val, found := strings.Cut(string(line), ":")
		if !found {
			if key == "end_of_record" {
				flush()
				cur = nil
			} else {
				fail()
			}
			continue
		}
		if key == "SF" {
			flush()
			cur = c.file(val, "")
			continue
		}
		if key == "TN" || key == "VER" || key == "FNL" {
			continue
		}
		if cur == nil {
			fail()
			continue
		}
		switch key {
		case "DA":
			f := strings.SplitN(val, ",", 3)
			n, ok := parseLineNo(f[0])
			if !ok || len(f) < 2 {
				fail()
				continue
			}
			cur.has |= hasLines
			cur.lines = append(cur.lines, lineRec{n, parseCount(f[1])})
		case "BRDA":
			f := strings.Split(val, ",")
			if len(f) < 4 {
				fail()
				continue
			}
			n, ok := parseLineNo(f[0])
			if !ok {
				fail()
				continue
			}
			taken := parseCount(f[len(f)-1]) // "-" (never reached) reads as 0
			cur.has |= hasBranches
			cur.branches = append(cur.branches, brRec{
				line: n, block: keyOf(f[1]), branch: keyOf(strings.Join(f[2:len(f)-1], ",")),
				cov: b2i(taken > 0), tot: 1,
			})
		case "FN":
			// FN:<line>,<name> or, since lcov 2.2, FN:<line>,<end line>,<name>.
			first, rest, ok := strings.Cut(val, ",")
			if _, okN := parseLineNo(first); !ok || !okN {
				fail()
				continue
			}
			if second, name, ok := strings.Cut(rest, ","); ok {
				if _, isNum := parseLineNo(second); isNum {
					rest = name
				}
			}
			cur.has |= hasFuncs
			cur.funcs = append(cur.funcs, fnRec{name: rawCut(rest, 4*lim.MaxPathBytes)})
		case "FNDA":
			hits, name, ok := strings.Cut(val, ",")
			if !ok {
				fail()
				continue
			}
			cur.has |= hasFuncs
			cur.funcs = append(cur.funcs, fnRec{name: rawCut(name, 4*lim.MaxPathBytes), hits: parseCount(hits)})
		case "FNA":
			f := strings.SplitN(val, ",", 3)
			if len(f) < 3 {
				fail()
				continue
			}
			cur.has |= hasFuncs
			cur.funcs = append(cur.funcs, fnRec{name: rawCut(f[2], 4*lim.MaxPathBytes), hits: parseCount(f[1])})
		case "LF":
			rec.Lines.Total = addSat(rec.Lines.Total, parseCount(val))
		case "LH":
			rec.Lines.Covered = addSat(rec.Lines.Covered, parseCount(val))
		case "BRF":
			rec.Branches.Total = addSat(rec.Branches.Total, parseCount(val))
		case "BRH":
			rec.Branches.Covered = addSat(rec.Branches.Covered, parseCount(val))
		case "FNF":
			rec.Functions.Total = addSat(rec.Functions.Total, parseCount(val))
		case "FNH":
			rec.Functions.Covered = addSat(rec.Functions.Covered, parseCount(val))
		}
	}
	flush()
	s.warnings(c, bad, badAt)
	return c.finish(), nil
}

// keyOf turns an LCOV block or branch id into a number: itself when it is
// one, else a hash of the text (lcov 2 writes e0 for exception blocks and
// expressions for branches).
func keyOf(s string) int64 {
	s = strings.TrimSpace(s)
	if v, err := strconv.ParseInt(s, 10, 64); err == nil && v >= 0 {
		return v
	}
	h := fnv.New64a()
	h.Write([]byte(s))
	return -int64(h.Sum64()>>2) - 2 // negative, and never -1
}

// parseGo reads a Go coverprofile (go test -coverprofile, any mode). Go
// records statements per block, not lines, so the Lines counters count
// statements: a block's statements are covered when its count is above 0,
// as go tool cover -func counts them. A block listed several times (from
// several test binaries, or -coverpkg) is counted once, covered when any
// listing has a count above 0. Path is the file as the profile writes it
// (import path and file name); Package is its import-path directory.
func parseGo(dl *deadline, data []byte, lim Limits) (*Report, error) {
	c := newCollector(FormatGo, lim)
	c.unit = UnitStatements
	s := &lineScanner{data: data}
	bad, badAt := 0, 0
	for {
		if dl.tick() {
			if !c.any() {
				return nil, dl.stopError()
			}
			c.warn("parsing %s; coverage after line %d is missing", dl.what(), s.n)
			break
		}
		line, tooLong, ok := s.next()
		if !ok {
			break
		}
		if tooLong || len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("mode:")) {
			// The first line, and again wherever profiles were joined.
			continue
		}
		file, b, ok := parseGoLine(line)
		if !ok {
			bad++
			if badAt == 0 {
				badAt = s.n
			}
			continue
		}
		f := c.file(file, "")
		f.has |= hasBlocks
		f.blocks = append(f.blocks, b)
	}
	s.warnings(c, bad, badAt)
	return c.finish(), nil
}

// parseGoLine reads "file:startLine.startCol,endLine.endCol numStmts count".
func parseGoLine(line []byte) (string, blockRec, bool) {
	i := bytes.LastIndexByte(line, ':')
	if i <= 0 {
		return "", blockRec{}, false
	}
	fields := strings.Fields(string(line[i+1:]))
	if len(fields) != 3 {
		return "", blockRec{}, false
	}
	start, end, ok := strings.Cut(fields[0], ",")
	if !ok {
		return "", blockRec{}, false
	}
	var b blockRec
	var ok1, ok2, ok3, ok4 bool
	sl, sc, _ := strings.Cut(start, ".")
	el, ec, _ := strings.Cut(end, ".")
	b.sl, ok1 = parseLineNo(sl)
	b.sc, ok2 = parseLineNo(sc)
	b.el, ok3 = parseLineNo(el)
	b.ec, ok4 = parseLineNo(ec)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return "", blockRec{}, false
	}
	if !isNumber(fields[1]) || !isNumber(fields[2]) {
		return "", blockRec{}, false
	}
	b.stmts = parseCount(fields[1])
	b.count = parseCount(fields[2])
	return string(line[:i]), b, true
}

// isNumber says whether s reads as a number, however big.
func isNumber(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil || errors.Is(err, strconv.ErrRange)
}
