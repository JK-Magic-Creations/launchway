// Package eventexpr is the small expression language of events: the filters
// that say whether an event starts anything, the dedupe keys that say whether
// two events are the same, and the mappings that turn an event's body into a
// pipeline's inputs.
//
// It is deliberately not a programming language. An expression is evaluated
// against values it is given (the body, the headers, a run's result) and has
// no side effects, no loops, no calls out and no way to read anything it was
// not handed. Its size and the work it may do are limited, so a hostile body
// cannot make it slow. A path that is missing is null, not an error, so a
// filter on a field that some senders leave out simply does not match.
//
// Forms:
//
//	body.release.version            a path into the values given (also body.items[0].name)
//	"text" 'text' 12 1.5 true null  literals; [1, "a"] is a list
//	a == b  a != b  a < b ...       comparison (numbers compare as numbers, anything else as text)
//	a in [x, y]                     is one of
//	a contains b                    text contains text, or a list holds a value
//	a startsWith b, a endsWith b    text tests
//	a glob "release/*"              the same patterns as branches
//	a && b  a || b  !a  (a)         logic
//	lower(x) upper(x) trim(x) len(x) default(x, y) join(list, ",") string(x)
//
// The answer of a filter must be true or false; anything else is an error
// that the event's decision says.
package eventexpr

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// Limits.
const (
	MaxLength = 1000 // characters of one expression
	maxDepth  = 24   // nesting
	maxSteps  = 5000 // evaluation steps
	maxText   = 64 << 10
)

// ErrSyntax marks an expression that cannot be read; the message says where.
var ErrSyntax = errors.New("expression syntax")

// Expr is a parsed expression.
type Expr struct {
	src  string
	root node
}

// String returns the expression as written.
func (e *Expr) String() string { return e.src }

// Parse reads an expression. It does not run it.
func Parse(src string) (*Expr, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("%w: the expression is empty", ErrSyntax)
	}
	if len(src) > MaxLength {
		return nil, fmt.Errorf("%w: longer than %d characters", ErrSyntax, MaxLength)
	}
	p := &parser{src: src}
	p.next()
	n, err := p.or(0)
	if err != nil {
		return nil, err
	}
	if p.tok.kind != tEOF {
		return nil, p.errf("unexpected %q", p.tok.text)
	}
	return &Expr{src: src, root: n}, nil
}

// Eval runs the expression with the given named values (such as "body").
func (e *Expr) Eval(vars map[string]any) (any, error) {
	ev := &evaluator{vars: vars}
	return ev.eval(e.root)
}

// Bool runs a filter: the answer must be true or false.
func (e *Expr) Bool(vars map[string]any) (bool, error) {
	v, err := e.Eval(vars)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("the filter %q gave %s, not true or false", e.src, describe(v))
	}
	return b, nil
}

// Text runs an expression whose answer becomes the text of an input or a key.
// Null becomes the empty text; lists and mappings become JSON.
func (e *Expr) Text(vars map[string]any) (string, error) {
	v, err := e.Eval(vars)
	if err != nil {
		return "", err
	}
	return Stringify(v), nil
}

// Stringify is how a value becomes the text of an input.
func Stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return formatNumber(x)
	case json.Number:
		return x.String()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func formatNumber(f float64) string {
	if f == float64(int64(f)) && f < 1e15 && f > -1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func describe(v any) string {
	switch v.(type) {
	case nil:
		return "nothing (the value is missing)"
	case string:
		return "text"
	case float64:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

// ---- syntax ----

type kind int

const (
	tEOF kind = iota
	tIdent
	tString
	tNumber
	tOp // punctuation and operators
)

type token struct {
	kind kind
	text string
	num  float64
	pos  int
}

type parser struct {
	src string
	pos int
	tok token
	err error
}

func (p *parser) errf(format string, a ...any) error {
	return fmt.Errorf("%w: %s (at character %d of %q)", ErrSyntax, fmt.Sprintf(format, a...), p.tok.pos+1, p.src)
}

func (p *parser) next() {
	for p.pos < len(p.src) && unicode.IsSpace(rune(p.src[p.pos])) {
		p.pos++
	}
	start := p.pos
	if p.pos >= len(p.src) {
		p.tok = token{kind: tEOF, pos: start}
		return
	}
	c := p.src[p.pos]
	switch {
	case c == '"' || c == '\'':
		q := c
		p.pos++
		var sb strings.Builder
		for p.pos < len(p.src) && p.src[p.pos] != q {
			if p.src[p.pos] == '\\' && p.pos+1 < len(p.src) {
				p.pos++
				switch p.src[p.pos] {
				case 'n':
					sb.WriteByte('\n')
				case 't':
					sb.WriteByte('\t')
				default:
					sb.WriteByte(p.src[p.pos])
				}
			} else {
				sb.WriteByte(p.src[p.pos])
			}
			p.pos++
		}
		if p.pos >= len(p.src) {
			p.tok = token{kind: tOp, text: "unterminated", pos: start}
			p.err = fmt.Errorf("%w: the text starting at character %d is not closed", ErrSyntax, start+1)
			return
		}
		p.pos++
		p.tok = token{kind: tString, text: sb.String(), pos: start}
	case c >= '0' && c <= '9' || (c == '-' && p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9'):
		p.pos++
		for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
			p.pos++
		}
		f, err := strconv.ParseFloat(p.src[start:p.pos], 64)
		if err != nil {
			p.err = fmt.Errorf("%w: %q is not a number", ErrSyntax, p.src[start:p.pos])
		}
		p.tok = token{kind: tNumber, num: f, text: p.src[start:p.pos], pos: start}
	case unicode.IsLetter(rune(c)) || c == '_':
		for p.pos < len(p.src) && (unicode.IsLetter(rune(p.src[p.pos])) || unicode.IsDigit(rune(p.src[p.pos])) || p.src[p.pos] == '_' || p.src[p.pos] == '-' && p.pos+1 < len(p.src) && unicode.IsLetter(rune(p.src[p.pos+1]))) {
			p.pos++
		}
		p.tok = token{kind: tIdent, text: p.src[start:p.pos], pos: start}
	default:
		two := ""
		if p.pos+1 < len(p.src) {
			two = p.src[p.pos : p.pos+2]
		}
		switch two {
		case "==", "!=", "<=", ">=", "&&", "||":
			p.pos += 2
			p.tok = token{kind: tOp, text: two, pos: start}
			return
		}
		if strings.ContainsRune("<>!()[],.", rune(c)) {
			p.pos++
			p.tok = token{kind: tOp, text: string(c), pos: start}
			return
		}
		p.pos++
		p.tok = token{kind: tOp, text: string(c), pos: start}
		p.err = fmt.Errorf("%w: unexpected %q at character %d", ErrSyntax, string(c), start+1)
	}
}

type node interface{}

type (
	lit  struct{ v any }
	list struct{ items []node }
	ref  struct{ steps []step } // the first step is a name
	call struct {
		fn   string
		args []node
	}
	unary  struct{ x node }
	binary struct {
		op   string
		l, r node
	}
)

type step struct {
	name  string
	index int
	isIdx bool
}

func (p *parser) or(depth int) (node, error) {
	if depth > maxDepth {
		return nil, p.errf("nested too deeply")
	}
	l, err := p.and(depth)
	if err != nil {
		return nil, err
	}
	for p.tok.kind == tOp && p.tok.text == "||" {
		p.next()
		r, err := p.and(depth)
		if err != nil {
			return nil, err
		}
		l = &binary{"||", l, r}
	}
	return l, nil
}

func (p *parser) and(depth int) (node, error) {
	l, err := p.not(depth)
	if err != nil {
		return nil, err
	}
	for p.tok.kind == tOp && p.tok.text == "&&" {
		p.next()
		r, err := p.not(depth)
		if err != nil {
			return nil, err
		}
		l = &binary{"&&", l, r}
	}
	return l, nil
}

func (p *parser) not(depth int) (node, error) {
	if p.tok.kind == tOp && p.tok.text == "!" {
		p.next()
		x, err := p.not(depth + 1)
		if err != nil {
			return nil, err
		}
		return &unary{x}, nil
	}
	return p.cmp(depth)
}

var wordOps = map[string]bool{"in": true, "contains": true, "startsWith": true, "endsWith": true, "glob": true}

func (p *parser) cmp(depth int) (node, error) {
	l, err := p.primary(depth)
	if err != nil {
		return nil, err
	}
	op := ""
	switch {
	case p.tok.kind == tOp && (p.tok.text == "==" || p.tok.text == "!=" || p.tok.text == "<" || p.tok.text == "<=" || p.tok.text == ">" || p.tok.text == ">="):
		op = p.tok.text
	case p.tok.kind == tIdent && wordOps[p.tok.text]:
		op = p.tok.text
	}
	if op == "" {
		return l, nil
	}
	p.next()
	r, err := p.primary(depth)
	if err != nil {
		return nil, err
	}
	return &binary{op, l, r}, nil
}

func (p *parser) primary(depth int) (node, error) {
	if p.err != nil {
		return nil, p.err
	}
	if depth > maxDepth {
		return nil, p.errf("nested too deeply")
	}
	t := p.tok
	switch {
	case t.kind == tString:
		p.next()
		return &lit{t.text}, nil
	case t.kind == tNumber:
		p.next()
		return &lit{t.num}, nil
	case t.kind == tOp && t.text == "(":
		p.next()
		n, err := p.or(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.tok.kind != tOp || p.tok.text != ")" {
			return nil, p.errf("expected ) but found %q", p.tok.text)
		}
		p.next()
		return n, nil
	case t.kind == tOp && t.text == "[":
		p.next()
		var items []node
		for !(p.tok.kind == tOp && p.tok.text == "]") {
			n, err := p.or(depth + 1)
			if err != nil {
				return nil, err
			}
			items = append(items, n)
			if p.tok.kind == tOp && p.tok.text == "," {
				p.next()
			} else if !(p.tok.kind == tOp && p.tok.text == "]") {
				return nil, p.errf("expected , or ] but found %q", p.tok.text)
			}
		}
		p.next()
		return &list{items}, nil
	case t.kind == tIdent:
		p.next()
		switch t.text {
		case "true":
			return &lit{true}, nil
		case "false":
			return &lit{false}, nil
		case "null":
			return &lit{nil}, nil
		}
		if p.tok.kind == tOp && p.tok.text == "(" {
			p.next()
			var args []node
			for !(p.tok.kind == tOp && p.tok.text == ")") {
				n, err := p.or(depth + 1)
				if err != nil {
					return nil, err
				}
				args = append(args, n)
				if p.tok.kind == tOp && p.tok.text == "," {
					p.next()
				} else if !(p.tok.kind == tOp && p.tok.text == ")") {
					return nil, p.errf("expected , or ) but found %q", p.tok.text)
				}
			}
			p.next()
			if _, ok := funcs[t.text]; !ok {
				return nil, fmt.Errorf("%w: there is no function %s (there are %s)", ErrSyntax, t.text, funcNames())
			}
			return &call{t.text, args}, nil
		}
		r := &ref{steps: []step{{name: t.text}}}
		for {
			switch {
			case p.tok.kind == tOp && p.tok.text == ".":
				p.next()
				if p.tok.kind != tIdent && p.tok.kind != tNumber {
					return nil, p.errf("expected a field name after .")
				}
				r.steps = append(r.steps, step{name: p.tok.text})
				p.next()
			case p.tok.kind == tOp && p.tok.text == "[":
				p.next()
				if p.tok.kind == tString {
					r.steps = append(r.steps, step{name: p.tok.text})
				} else if p.tok.kind == tNumber {
					r.steps = append(r.steps, step{index: int(p.tok.num), isIdx: true})
				} else {
					return nil, p.errf("expected a number or text in []")
				}
				p.next()
				if p.tok.kind != tOp || p.tok.text != "]" {
					return nil, p.errf("expected ]")
				}
				p.next()
			default:
				if len(r.steps) > 24 {
					return nil, p.errf("a path of more than 24 fields")
				}
				return r, nil
			}
		}
	}
	if p.tok.kind == tEOF {
		return nil, p.errf("the expression ends too soon")
	}
	return nil, p.errf("unexpected %q", p.tok.text)
}

// ---- evaluation ----

type evaluator struct {
	vars  map[string]any
	steps int
}

var errSteps = errors.New("the expression did more work than it may")

func (ev *evaluator) eval(n node) (any, error) {
	ev.steps++
	if ev.steps > maxSteps {
		return nil, errSteps
	}
	switch x := n.(type) {
	case *lit:
		return x.v, nil
	case *list:
		out := make([]any, 0, len(x.items))
		for _, it := range x.items {
			v, err := ev.eval(it)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case *ref:
		cur, ok := ev.vars[x.steps[0].name]
		if !ok {
			return nil, fmt.Errorf("there is no value called %s here (there is %s)", x.steps[0].name, varNames(ev.vars))
		}
		for _, s := range x.steps[1:] {
			switch c := cur.(type) {
			case map[string]any:
				cur = c[s.name]
			case []any:
				i := s.index
				if !s.isIdx {
					n, err := strconv.Atoi(s.name)
					if err != nil {
						return nil, nil
					}
					i = n
				}
				if i < 0 || i >= len(c) {
					return nil, nil
				}
				cur = c[i]
			default:
				return nil, nil // a field of something that is not there is not there
			}
		}
		return cur, nil
	case *unary:
		b, err := ev.boolOf(x.x, "!")
		return !b, err
	case *call:
		args := make([]any, len(x.args))
		for i, a := range x.args {
			v, err := ev.eval(a)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		return funcs[x.fn](args)
	case *binary:
		return ev.binary(x)
	}
	return nil, fmt.Errorf("unknown expression %T", n)
}

func (ev *evaluator) boolOf(n node, op string) (bool, error) {
	v, err := ev.eval(n)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s needs true or false, and got %s", op, describe(v))
	}
	return b, nil
}

func (ev *evaluator) binary(x *binary) (any, error) {
	switch x.op {
	case "&&", "||":
		l, err := ev.boolOf(x.l, x.op)
		if err != nil {
			return nil, err
		}
		if x.op == "&&" && !l {
			return false, nil
		}
		if x.op == "||" && l {
			return true, nil
		}
		return ev.boolOf(x.r, x.op)
	}
	l, err := ev.eval(x.l)
	if err != nil {
		return nil, err
	}
	r, err := ev.eval(x.r)
	if err != nil {
		return nil, err
	}
	switch x.op {
	case "==":
		return equal(l, r), nil
	case "!=":
		return !equal(l, r), nil
	case "<", "<=", ">", ">=":
		lf, lok := number(l)
		rf, rok := number(r)
		if !lok || !rok {
			if l == nil || r == nil {
				return false, nil
			}
			ls, rs := Stringify(l), Stringify(r)
			c := strings.Compare(ls, rs)
			return cmpResult(x.op, c), nil
		}
		c := 0
		if lf < rf {
			c = -1
		} else if lf > rf {
			c = 1
		}
		return cmpResult(x.op, c), nil
	case "in":
		items, ok := r.([]any)
		if !ok {
			return nil, fmt.Errorf("in needs a list on the right, and got %s", describe(r))
		}
		for _, it := range items {
			if equal(l, it) {
				return true, nil
			}
		}
		return false, nil
	case "contains":
		if items, ok := l.([]any); ok {
			for _, it := range items {
				if equal(it, r) {
					return true, nil
				}
			}
			return false, nil
		}
		if l == nil || r == nil {
			return false, nil
		}
		return strings.Contains(Stringify(l), Stringify(r)), nil
	case "startsWith":
		return l != nil && r != nil && strings.HasPrefix(Stringify(l), Stringify(r)), nil
	case "endsWith":
		return l != nil && r != nil && strings.HasSuffix(Stringify(l), Stringify(r)), nil
	case "glob":
		if l == nil || r == nil {
			return false, nil
		}
		ok, err := path.Match(Stringify(r), Stringify(l))
		if err != nil {
			return nil, fmt.Errorf("%q is not a pattern: %v", Stringify(r), err)
		}
		return ok, nil
	}
	return nil, fmt.Errorf("unknown operator %s", x.op)
}

func cmpResult(op string, c int) bool {
	switch op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	}
	return c >= 0
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func equal(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if af, ok := a.(float64); ok {
		if bf, ok := number(b); ok {
			return af == bf
		}
	}
	if bf, ok := b.(float64); ok {
		if af, ok := number(a); ok {
			return af == bf
		}
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return as == bs
	}
	if ab, ok := a.(bool); ok {
		if bb, ok := b.(bool); ok {
			return ab == bb
		}
		return false
	}
	return Stringify(a) == Stringify(b) && aok == bok
}

func varNames(vars map[string]any) string {
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sortStrings(names)
	return strings.Join(names, ", ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var funcs map[string]func([]any) (any, error)

func funcNames() string {
	names := make([]string, 0, len(funcs))
	for k := range funcs {
		names = append(names, k)
	}
	sortStrings(names)
	return strings.Join(names, ", ")
}

func init() {
	one := func(f func(any) (any, error)) func([]any) (any, error) {
		return func(a []any) (any, error) {
			if len(a) != 1 {
				return nil, errors.New("this function takes one value")
			}
			return f(a[0])
		}
	}
	funcs = map[string]func([]any) (any, error){
		"lower":  one(func(v any) (any, error) { return strings.ToLower(Stringify(v)), nil }),
		"upper":  one(func(v any) (any, error) { return strings.ToUpper(Stringify(v)), nil }),
		"trim":   one(func(v any) (any, error) { return strings.TrimSpace(Stringify(v)), nil }),
		"string": one(func(v any) (any, error) { return Stringify(v), nil }),
		"len": one(func(v any) (any, error) {
			switch x := v.(type) {
			case nil:
				return float64(0), nil
			case []any:
				return float64(len(x)), nil
			case map[string]any:
				return float64(len(x)), nil
			}
			return float64(len([]rune(Stringify(v)))), nil
		}),
		"default": func(a []any) (any, error) {
			if len(a) != 2 {
				return nil, errors.New("default takes a value and what to use when it is missing")
			}
			if a[0] == nil || a[0] == "" {
				return a[1], nil
			}
			return a[0], nil
		},
		"join": func(a []any) (any, error) {
			if len(a) != 2 {
				return nil, errors.New("join takes a list and a separator")
			}
			items, ok := a[0].([]any)
			if !ok {
				return Stringify(a[0]), nil
			}
			parts := make([]string, len(items))
			for i, it := range items {
				parts[i] = Stringify(it)
			}
			s := strings.Join(parts, Stringify(a[1]))
			if len(s) > maxText {
				return nil, errors.New("the joined text is too long")
			}
			return s, nil
		},
	}
}
