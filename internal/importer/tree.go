package importer

// A small, safe tree for the YAML the readers look at. The YAML library's own
// nodes are copied into it once, with aliases and merge keys resolved under a
// budget, so a file that expands to billions of nodes ("alias bomb"), is nested
// too deeply or is too large is refused before any reader sees it. Nothing in a
// file is ever evaluated.

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limits bound what a reader accepts. They are the same for every format.
type Limits struct {
	MaxBytes int // size of one file
	MaxDepth int // nesting of mappings and lists
	MaxNodes int // nodes in the tree after aliases are expanded
}

// DefaultLimits are the limits used by the CLI, the API and the tests.
var DefaultLimits = Limits{MaxBytes: 256 << 10, MaxDepth: 48, MaxNodes: 100_000}

type kind int

const (
	kNull kind = iota
	kScalar
	kSeq
	kMap
)

// N is one value of a YAML file with the line it starts on.
type N struct {
	Kind    kind
	Val     string // scalars
	Quoted  bool   // a quoted scalar is text, never a number or boolean
	Line    int
	Keys    []string
	KeyLine []int
	Vals    []*N
	Items   []*N
}

// Get returns the value under a key of a mapping, or nil. It is safe on nil.
func (n *N) Get(key string) *N {
	if n == nil || n.Kind != kMap {
		return nil
	}
	for i, k := range n.Keys {
		if k == key {
			return n.Vals[i]
		}
	}
	return nil
}

// Has says whether a mapping has the key.
func (n *N) Has(key string) bool { return n.Get(key) != nil }

// Str is a scalar's text, or "".
func (n *N) Str() string {
	if n == nil || n.Kind != kScalar {
		return ""
	}
	return n.Val
}

// IsMap, IsSeq and IsScalar test the shape.
func (n *N) IsMap() bool    { return n != nil && n.Kind == kMap }
func (n *N) IsSeq() bool    { return n != nil && n.Kind == kSeq }
func (n *N) IsScalar() bool { return n != nil && n.Kind == kScalar }

// Strings returns a scalar as one item, or the scalar items of a list.
func (n *N) Strings() []string {
	switch {
	case n == nil:
		return nil
	case n.Kind == kScalar:
		return []string{n.Val}
	case n.Kind == kSeq:
		var out []string
		for _, it := range n.Items {
			if it.Kind == kScalar {
				out = append(out, it.Val)
			}
		}
		return out
	}
	return nil
}

// Bool reads true/false/yes/no scalars.
func (n *N) Bool() (v, ok bool) {
	if n == nil || n.Kind != kScalar || n.Quoted {
		return false, false
	}
	switch strings.ToLower(n.Val) {
	case "true", "yes", "on":
		return true, true
	case "false", "no", "off":
		return false, true
	}
	return false, false
}

// LineOf is the line of a key, or the line of the node itself.
func (n *N) LineOf(key string) int {
	if n == nil {
		return 0
	}
	for i, k := range n.Keys {
		if k == key {
			return n.KeyLine[i]
		}
	}
	return n.Line
}

type conv struct {
	lim   Limits
	nodes int
}

// ErrTooLarge and friends are the refusals a hostile or oversized file gets.
var (
	ErrTooLarge = errors.New("the file is too large to read here")
	ErrTooDeep  = errors.New("the file nests mappings or lists too deeply")
	ErrTooMany  = errors.New("the file holds too many values once its anchors and aliases are expanded")
)

// ParseTree reads the first YAML document of data into a tree under limits.
func ParseTree(data []byte, lim Limits) (*N, error) {
	if lim.MaxBytes == 0 {
		lim = DefaultLimits
	}
	if len(data) > lim.MaxBytes {
		return nil, fmt.Errorf("%w (%d bytes; at most %d)", ErrTooLarge, len(data), lim.MaxBytes)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("the file is not valid YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return &N{Kind: kNull, Line: 1}, nil
	}
	c := &conv{lim: lim}
	return c.node(doc.Content[0], 0)
}

func (c *conv) node(y *yaml.Node, depth int) (*N, error) {
	if depth > c.lim.MaxDepth {
		return nil, fmt.Errorf("%w (line %d; at most %d levels)", ErrTooDeep, y.Line, c.lim.MaxDepth)
	}
	c.nodes++
	if c.nodes > c.lim.MaxNodes {
		return nil, fmt.Errorf("%w (at most %d)", ErrTooMany, c.lim.MaxNodes)
	}
	switch y.Kind {
	case yaml.AliasNode:
		return c.node(y.Alias, depth) // counted again, so a bomb runs out of budget
	case yaml.ScalarNode:
		if y.Tag == "!!null" {
			return &N{Kind: kNull, Line: y.Line}, nil
		}
		return &N{Kind: kScalar, Val: y.Value, Quoted: y.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0, Line: y.Line}, nil
	case yaml.SequenceNode:
		n := &N{Kind: kSeq, Line: y.Line}
		for _, it := range y.Content {
			v, err := c.node(it, depth+1)
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, v)
		}
		return n, nil
	case yaml.MappingNode:
		n := &N{Kind: kMap, Line: y.Line}
		for i := 0; i+1 < len(y.Content); i += 2 {
			k, v := y.Content[i], y.Content[i+1]
			if k.Kind == yaml.ScalarNode && k.Value == "<<" && k.Tag == "!!merge" {
				// A merge key: the keys of the mapping (or each of a list of
				// mappings) are added unless this mapping already has them.
				var srcs []*N
				vv := v
				if vv.Kind == yaml.AliasNode {
					vv = vv.Alias
				}
				list := []*yaml.Node{vv}
				if vv.Kind == yaml.SequenceNode {
					list = vv.Content
				}
				for _, s := range list {
					m, err := c.node(s, depth+1)
					if err != nil {
						return nil, err
					}
					if m.Kind == kMap {
						srcs = append(srcs, m)
					}
				}
				for _, m := range srcs {
					for j, mk := range m.Keys {
						if n.Get(mk) == nil && !hasLaterKey(y, i, mk) {
							n.Keys, n.KeyLine, n.Vals = append(n.Keys, mk), append(n.KeyLine, m.KeyLine[j]), append(n.Vals, m.Vals[j])
						}
					}
				}
				continue
			}
			vn, err := c.node(v, depth+1)
			if err != nil {
				return nil, err
			}
			key := k.Value
			if k.Kind != yaml.ScalarNode {
				key = fmt.Sprintf("(complex key at line %d)", k.Line)
			}
			n.Keys, n.KeyLine, n.Vals = append(n.Keys, key), append(n.KeyLine, k.Line), append(n.Vals, vn)
		}
		return n, nil
	}
	return &N{Kind: kNull, Line: y.Line}, nil
}

// hasLaterKey says whether the mapping writes the key itself after a merge key.
func hasLaterKey(y *yaml.Node, from int, key string) bool {
	for i := from + 2; i+1 < len(y.Content); i += 2 {
		if y.Content[i].Value == key {
			return true
		}
	}
	return false
}

// List is the items of a list, or nil; safe on nil.
func (n *N) List() []*N {
	if n == nil {
		return nil
	}
	return n.Items
}

// Pairs calls f for each entry of a mapping; safe on nil.
func (n *N) Pairs(f func(key string, v *N, line int)) {
	if n == nil || n.Kind != kMap {
		return
	}
	for i, k := range n.Keys {
		f(k, n.Vals[i], n.KeyLine[i])
	}
}
