package importer

// A reader's tokenizer and parser for the declarative Groovy pipeline file. It
// reads the shape of the file only: statements, blocks, arguments and literal
// strings. It never evaluates anything and never runs Groovy: an expression it
// cannot read as a literal is kept as raw text and reported. Sizes and nesting
// are bounded so hostile input cannot exhaust memory or the stack.

import (
	"fmt"
	"strings"
)

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tStr
	tNum
	tPunct
	tNL
)

type token struct {
	kind       tokKind
	text       string // identifiers, punctuation, and the unquoted body of strings
	interp     bool   // a double-quoted string (Groovy interpolates $ in it)
	line       int
	start, end int // byte offsets in the source
}

const (
	maxTokens = 200_000
	maxGDepth = 40
	maxStmts  = 50_000
)

type gerr struct {
	msg  string
	line int
}

func (e *gerr) Error() string { return fmt.Sprintf("line %d: %s", e.line, e.msg) }

func lexGroovy(src string) ([]token, error) {
	var toks []token
	line := 1
	i := 0
	emit := func(t token) error {
		if len(toks) >= maxTokens {
			return &gerr{"the file has too many tokens", line}
		}
		toks = append(toks, t)
		return nil
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			if err := emit(token{kind: tNL, line: line, start: i, end: i + 1}); err != nil {
				return nil, err
			}
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\\' && i+1 < len(src) && src[i+1] == '\n':
			i += 2
			line++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			end := len(src)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			line += strings.Count(src[i:end], "\n")
			i = end
		case c == '#' && i == 0 && strings.HasPrefix(src, "#!"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '\'' || c == '"':
			start := i
			q := string(c)
			triple := strings.HasPrefix(src[i:], q+q+q)
			var body strings.Builder
			if triple {
				i += 3
			} else {
				i++
			}
			startLine := line
			closed := false
			for i < len(src) {
				if triple && strings.HasPrefix(src[i:], q+q+q) {
					i += 3
					closed = true
					break
				}
				if !triple && src[i] == c {
					i++
					closed = true
					break
				}
				if !triple && src[i] == '\n' {
					break
				}
				if src[i] == '\\' && i+1 < len(src) {
					n := src[i+1]
					switch n {
					case 'n':
						body.WriteByte('\n')
					case 't':
						body.WriteByte('\t')
					case '\n':
						line++
					default:
						body.WriteByte(n)
					}
					i += 2
					continue
				}
				if src[i] == '\n' {
					line++
				}
				body.WriteByte(src[i])
				i++
			}
			if !closed {
				return nil, &gerr{"a string is not closed", startLine}
			}
			if err := emit(token{kind: tStr, text: body.String(), interp: c == '"', line: startLine, start: start, end: i}); err != nil {
				return nil, err
			}
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == '_' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z') {
				j++
			}
			if err := emit(token{kind: tNum, text: src[i:j], line: line, start: i, end: j}); err != nil {
				return nil, err
			}
			i = j
		case c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(src) && (src[j] == '_' || src[j] == '$' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			if err := emit(token{kind: tIdent, text: src[i:j], line: line, start: i, end: j}); err != nil {
				return nil, err
			}
			i = j
		default:
			n := 1
			if i+1 < len(src) {
				switch src[i : i+2] {
				case "==", "!=", "<=", ">=", "&&", "||", "->", "?.", "+=", "-=", "=~", "..", "?:", "<<":
					n = 2
				}
			}
			if err := emit(token{kind: tPunct, text: src[i : i+n], line: line, start: i, end: i + n}); err != nil {
				return nil, err
			}
			i += n
		}
	}
	toks = append(toks, token{kind: tEOF, line: line, start: len(src), end: len(src)})
	return toks, nil
}

// gx is an argument: a literal, a list, a map, a name, a call or raw text.
type gx struct {
	kind   byte // 's' string, 'n' number, 'i' name, 'l' list, 'm' map, 'k' named argument, 'c' call, 'r' raw
	text   string
	interp bool
	items  []*gx
	line   int
}

// gs is a statement: a name, its arguments and an optional block.
type gs struct {
	name     string
	args     []*gx
	block    []*gs
	hasBlock bool
	raw      string // the source of the block, for blocks that are not read
	line     int
}

// named returns the value of a named argument, or nil.
func (s *gs) named(key string) *gx {
	for _, a := range s.args {
		if a.kind == 'k' && a.text == key {
			return a.items[0]
		}
		if a.kind == 'm' {
			for _, it := range a.items {
				if it.kind == 'k' && it.text == key {
					return it.items[0]
				}
			}
		}
	}
	return nil
}

// first is the first positional argument as text.
func (s *gs) first() *gx {
	for _, a := range s.args {
		if a.kind != 'k' {
			return a
		}
	}
	return nil
}

func (g *gx) str() string {
	if g == nil {
		return ""
	}
	if g.kind == 's' || g.kind == 'n' || g.kind == 'i' {
		return g.text
	}
	return ""
}

func (g *gx) strs() []string {
	if g == nil {
		return nil
	}
	if g.kind == 'l' {
		var out []string
		for _, it := range g.items {
			if it.kind == 's' {
				out = append(out, it.text)
			}
		}
		return out
	}
	if g.kind == 's' {
		return []string{g.text}
	}
	return nil
}

type gparser struct {
	toks  []token
	pos   int
	src   string
	nstmt int
}

func (p *gparser) peek() token { return p.toks[p.pos] }
func (p *gparser) next() token {
	t := p.toks[p.pos]
	if t.kind != tEOF {
		p.pos++
	}
	return t
}
func (p *gparser) isP(s string) bool { t := p.peek(); return t.kind == tPunct && t.text == s }
func (p *gparser) skipNL() {
	for p.peek().kind == tNL || p.isP(";") {
		p.pos++
	}
}

// parseBlock reads statements up to the closing brace (already past the opening one).
func (p *gparser) parseBlock(depth int) ([]*gs, error) {
	if depth > maxGDepth {
		return nil, &gerr{"the file nests blocks too deeply", p.peek().line}
	}
	var out []*gs
	for {
		p.skipNL()
		t := p.peek()
		switch {
		case t.kind == tEOF:
			return out, nil
		case t.kind == tPunct && t.text == "}":
			p.pos++
			return out, nil
		}
		s, err := p.parseStmt(depth)
		if err != nil {
			return nil, err
		}
		if s != nil {
			out = append(out, s)
		}
	}
}

func (p *gparser) parseStmt(depth int) (*gs, error) {
	p.nstmt++
	if p.nstmt > maxStmts {
		return nil, &gerr{"the file has too many statements", p.peek().line}
	}
	t := p.peek()
	if t.kind != tIdent {
		// Not a call: keep the rest of the line as raw text.
		return p.rawLine(), nil
	}
	p.pos++
	name := t.text
	for p.isP(".") && p.toks[p.pos+1].kind == tIdent {
		p.pos++
		name += "." + p.next().text
	}
	s := &gs{name: name, line: t.line}
	switch {
	case p.isP("("):
		args, err := p.parseArgs(")", depth)
		if err != nil {
			return nil, err
		}
		s.args = args
	case p.isP("{") || p.peek().kind == tNL || p.isP("}") || p.isP(";") || p.peek().kind == tEOF:
	case p.isP("=") || p.isP("+=") || p.isP("-="):
		return p.rawFrom(t), nil
	default:
		args, err := p.parseCmdArgs(depth)
		if err != nil {
			return nil, err
		}
		s.args = args
	}
	if p.isP("{") {
		open := p.next()
		s.hasBlock = true
		if name == "script" || name == "node" && false {
			start := open.end
			d := 1
			for d > 0 && p.peek().kind != tEOF {
				tk := p.next()
				if tk.kind == tPunct && tk.text == "{" {
					d++
				} else if tk.kind == tPunct && tk.text == "}" {
					d--
				}
			}
			end := p.toks[p.pos-1].start
			if end < start {
				end = start
			}
			s.raw = strings.TrimSpace(p.src[start:end])
			return s, nil
		}
		b, err := p.parseBlock(depth + 1)
		if err != nil {
			return nil, err
		}
		s.block = b
	}
	return s, nil
}

func (p *gparser) rawLine() *gs {
	t := p.peek()
	start := t.start
	for p.peek().kind != tNL && p.peek().kind != tEOF {
		if p.isP("}") {
			break
		}
		p.pos++
	}
	end := p.toks[p.pos].start
	return &gs{name: "(raw)", line: t.line, raw: strings.TrimSpace(p.src[start:end])}
}

func (p *gparser) rawFrom(first token) *gs {
	start := first.start
	for p.peek().kind != tNL && p.peek().kind != tEOF && !p.isP("}") {
		p.pos++
	}
	return &gs{name: "(raw)", line: first.line, raw: strings.TrimSpace(p.src[start:p.toks[p.pos].start])}
}

func (p *gparser) parseArgs(close string, depth int) ([]*gx, error) {
	p.pos++ // the opening bracket
	var out []*gx
	for {
		for p.peek().kind == tNL {
			p.pos++
		}
		if p.isP(close) {
			p.pos++
			return out, nil
		}
		if p.peek().kind == tEOF {
			return nil, &gerr{"a bracket is not closed", p.peek().line}
		}
		a, err := p.parseArg(depth)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
		for p.peek().kind == tNL {
			p.pos++
		}
		if p.isP(",") {
			p.pos++
		}
	}
}

func (p *gparser) parseCmdArgs(depth int) ([]*gx, error) {
	var out []*gx
	for {
		a, err := p.parseArg(depth)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
		if p.isP(",") {
			p.pos++
			for p.peek().kind == tNL {
				p.pos++
			}
			continue
		}
		return out, nil
	}
}

func (p *gparser) parseArg(depth int) (*gx, error) {
	if depth > maxGDepth {
		return nil, &gerr{"the file nests values too deeply", p.peek().line}
	}
	t := p.peek()
	// named argument: key: value
	if (t.kind == tIdent || t.kind == tStr) && p.toks[p.pos+1].kind == tPunct && p.toks[p.pos+1].text == ":" {
		p.pos += 2
		v, err := p.parseExpr(depth + 1)
		if err != nil {
			return nil, err
		}
		return &gx{kind: 'k', text: t.text, items: []*gx{v}, line: t.line}, nil
	}
	return p.parseExpr(depth + 1)
}

func (p *gparser) parseExpr(depth int) (*gx, error) {
	if depth > maxGDepth {
		return nil, &gerr{"the file nests values too deeply", p.peek().line}
	}
	startOff := p.peek().start
	first, err := p.primary(depth)
	if err != nil {
		return nil, err
	}
	// Operators and anything else glue the rest of the expression into raw text.
	t := p.peek()
	if t.kind == tPunct && t.text != "," && t.text != ")" && t.text != "]" && t.text != "}" && t.text != "{" && t.text != ":" && t.text != "->" && t.text != ";" {
		for {
			t := p.peek()
			if t.kind == tEOF || t.kind == tNL || t.kind == tPunct && (t.text == "," || t.text == ")" || t.text == "]" || t.text == "}" || t.text == "{" || t.text == ";") {
				break
			}
			if t.kind == tPunct && (t.text == "(" || t.text == "[") {
				if _, err := p.parseArgs(map[string]string{"(": ")", "[": "]"}[t.text], depth+1); err != nil {
					return nil, err
				}
				continue
			}
			p.pos++
		}
		end := p.toks[p.pos].start
		return &gx{kind: 'r', text: strings.TrimSpace(p.src[startOff:end]), line: first.line}, nil
	}
	return first, nil
}

func (p *gparser) primary(depth int) (*gx, error) {
	t := p.next()
	switch t.kind {
	case tStr:
		return &gx{kind: 's', text: t.text, interp: t.interp, line: t.line}, nil
	case tNum:
		return &gx{kind: 'n', text: t.text, line: t.line}, nil
	case tIdent:
		name := t.text
		for p.isP(".") && p.toks[p.pos+1].kind == tIdent {
			p.pos++
			name += "." + p.next().text
		}
		if p.isP("(") {
			args, err := p.parseArgs(")", depth+1)
			if err != nil {
				return nil, err
			}
			return &gx{kind: 'c', text: name, items: args, line: t.line}, nil
		}
		return &gx{kind: 'i', text: name, line: t.line}, nil
	case tPunct:
		if t.text == "[" {
			p.pos--
			items, err := p.parseArgs("]", depth+1)
			if err != nil {
				return nil, err
			}
			kind := byte('l')
			if len(items) > 0 && items[0].kind == 'k' {
				kind = 'm'
			}
			if len(items) == 0 && p.toks[p.pos-2].kind == tPunct && p.toks[p.pos-2].text == ":" {
				kind = 'm'
			}
			return &gx{kind: kind, items: items, line: t.line}, nil
		}
		if t.text == "(" {
			p.pos--
			items, err := p.parseArgs(")", depth+1)
			if err != nil {
				return nil, err
			}
			return &gx{kind: 'r', text: "(...)", items: items, line: t.line}, nil
		}
		if t.text == "{" {
			// a closure argument: skip to its end
			d := 1
			start := t.end
			for d > 0 && p.peek().kind != tEOF {
				tk := p.next()
				if tk.kind == tPunct && tk.text == "{" {
					d++
				} else if tk.kind == tPunct && tk.text == "}" {
					d--
				}
			}
			end := p.toks[p.pos-1].start
			if end < start {
				end = start
			}
			return &gx{kind: 'r', text: "{" + strings.TrimSpace(p.src[start:end]) + "}", line: t.line}, nil
		}
		return &gx{kind: 'r', text: t.text, line: t.line}, nil
	}
	return &gx{kind: 'r', text: t.text, line: t.line}, nil
}

// parseGroovy reads a whole file into statements.
func parseGroovy(src string) ([]*gs, error) {
	toks, err := lexGroovy(src)
	if err != nil {
		return nil, err
	}
	p := &gparser{toks: toks, src: src}
	var out []*gs
	for {
		p.skipNL()
		if p.peek().kind == tEOF {
			return out, nil
		}
		if p.isP("}") {
			p.pos++
			continue
		}
		s, err := p.parseStmt(0)
		if err != nil {
			return nil, err
		}
		if s != nil {
			out = append(out, s)
		}
	}
}
