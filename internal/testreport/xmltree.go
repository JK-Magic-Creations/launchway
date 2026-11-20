package testreport

import (
	"encoding/xml"
	"io"
	"strings"
)

// The XML formats other than JUnit (TestNG, NUnit, xUnit.net, TRX, Robot
// Framework) are read with one streaming walker. A format's handler sees
// each element as it starts and ends, and asks for the elements that hold
// one test (a <test-method>, a <test-case>) to be captured as a small tree.
// The tree is bounded: at most so many nodes and so many bytes of text,
// with the few direct children a format must see whatever happens (a Robot
// test's final <status>, its tags) kept even when the rest is cut.

// xnode is one element of a captured tree.
type xnode struct {
	name  string
	attrs []xml.Attr
	kids  []*xnode
	text  headBuf
	tail  *tailBuf // the end of the text, kept once the head is full
}

// attr returns the value of the attribute with this local name.
func (n *xnode) attr(name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *xnode) has(name string) bool {
	if n == nil {
		return false
	}
	for _, a := range n.attrs {
		if a.Name.Local == name {
			return true
		}
	}
	return false
}

// kid returns the first child with this name.
func (n *xnode) kid(name string) *xnode {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

// lastKid returns the last child with this name.
func (n *xnode) lastKid(name string) *xnode {
	if n == nil {
		return nil
	}
	for i := len(n.kids) - 1; i >= 0; i-- {
		if n.kids[i].name == name {
			return n.kids[i]
		}
	}
	return nil
}

// path follows a chain of first children.
func (n *xnode) path(names ...string) *xnode {
	for _, name := range names {
		n = n.kid(name)
	}
	return n
}

// all returns the children with this name.
func (n *xnode) all(name string) []*xnode {
	if n == nil {
		return nil
	}
	var out []*xnode
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
	}
	return out
}

// str returns the start of the element's own text and whether it was cut.
func (n *xnode) str() (string, bool) {
	if n == nil {
		return "", false
	}
	return string(n.text.b), n.text.over
}

// s returns the start of the element's own text.
func (n *xnode) s() string {
	t, _ := n.str()
	return t
}

// end returns the end of the element's text and whether some was dropped.
func (n *xnode) end() (string, bool) {
	if n == nil {
		return "", false
	}
	if n.tail == nil {
		return string(n.text.b), false
	}
	t, _ := n.tail.text()
	return t, true
}

// xmlHandler is what a format gives the walker.
type xmlHandler interface {
	// start is called for each element outside a captured tree; returning
	// true captures the element and everything in it as a tree.
	start(t xml.StartElement) bool
	// end is called when an element that was not captured ends.
	end(name string)
	// tree is called with each captured tree when it is complete.
	tree(n *xnode)
}

// xmlWalker drives a handler over a decoder.
type xmlWalker struct {
	d     *xml.Decoder
	dl    *deadline
	lim   Limits
	col   *collector
	h     xmlHandler
	vital map[string]bool // direct children of a captured element always kept

	depth int
	// the tree being captured: a stack of open nodes (nil for a node that
	// was left out), and what it has used
	root     *xnode
	cap      []*xnode
	capVital []bool
	stopped  bool        // the time ran out
	full     func() bool // there is no room for more cases (then trees are kept small)
	nodes    int
	textUsed int
	vitalN   int

	maxNodes, maxText, nodeText, tailText int
	small                                 bool // capture only the root and vital children (no room for more cases)
}

func newXMLWalker(dl *deadline, d *xml.Decoder, lim Limits, col *collector, h xmlHandler, vital ...string) *xmlWalker {
	w := &xmlWalker{d: d, dl: dl, lim: lim, col: col, h: h, vital: map[string]bool{}, full: col.full}
	for _, v := range vital {
		w.vital[v] = true
	}
	w.maxNodes = max(10000, 50*lim.MaxSteps)
	w.nodeText = 4 * max(lim.MaxDetailsBytes, lim.MaxStepTextBytes, lim.MaxMessageBytes, lim.MaxOutputBytes, MaxHTTPBody)
	w.tailText = 4 * lim.MaxOutputBytes
	w.maxText = 16 << 20
	return w
}

// run reads the document whose root element was already read.
func (w *xmlWalker) run(root xml.StartElement) {
	w.open(root)
	for {
		if w.dl.tick() {
			line, _ := w.d.InputPos()
			w.col.warn("parsing %s; results after line %d are missing", w.dl.what(), line)
			w.stopped = true
			return
		}
		tok, err := w.d.Token()
		if err != nil {
			if err == io.EOF && w.depth == 0 {
				return
			}
			line, msg := xmlErrorText(w.d, err)
			if strings.Contains(msg, "unexpected EOF") {
				w.col.warn("the file ends part-way through (XML error at line %d: %s); results after that are missing", line, msg)
			} else {
				w.col.warn("the XML is broken at line %d (%s); results after that are missing", line, msg)
			}
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if w.depth >= w.lim.MaxDepth {
				line, _ := w.d.InputPos()
				w.col.warn("the XML nests elements more than %d deep at line %d; results after that are missing", w.lim.MaxDepth, line)
				return
			}
			w.open(t)
		case xml.EndElement:
			if w.depth == 0 {
				continue
			}
			w.depth--
			if n := len(w.cap); n > 0 {
				w.cap = w.cap[:n-1]
				w.capVital = w.capVital[:n-1]
				if n == 1 {
					root := w.root
					w.root = nil
					w.h.tree(root)
				}
				continue
			}
			w.h.end(t.Name.Local)
		case xml.CharData:
			if n := len(w.cap); n > 0 && w.cap[n-1] != nil {
				w.text(w.cap[n-1], t)
			}
		}
	}
}

func (w *xmlWalker) open(t xml.StartElement) {
	w.depth++
	if len(w.cap) == 0 {
		if !w.h.start(t) {
			return
		}
		w.nodes, w.textUsed, w.vitalN = 0, 0, 0
		w.small = w.full()
		w.root = w.node(t)
		w.cap = append(w.cap, w.root)
		w.capVital = append(w.capVital, true)
		return
	}
	parent := w.cap[len(w.cap)-1]
	keep, vital := false, false
	if parent != nil {
		switch {
		case len(w.cap) == 1 && w.vital[t.Name.Local]:
			keep, vital = w.vitalN < 256, true
		case w.capVital[len(w.cap)-1] && len(w.cap) == 2:
			keep, vital = w.vitalN < 256, true // the children of a vital child (RF6 <tags><tag>)
		default:
			keep = !w.small && w.nodes < w.maxNodes
		}
	}
	if !keep {
		w.cap = append(w.cap, nil)
		w.capVital = append(w.capVital, false)
		return
	}
	n := w.node(t)
	if vital {
		w.vitalN++
	} else {
		w.nodes++
		if w.nodes == w.maxNodes {
			w.col.warnOnce("nodes", "some tests hold more than %d XML elements; the rest of each was left out", w.maxNodes)
		}
	}
	parent.kids = append(parent.kids, n)
	w.cap = append(w.cap, n)
	w.capVital = append(w.capVital, vital)
}

func (w *xmlWalker) node(t xml.StartElement) *xnode {
	n := &xnode{name: t.Name.Local, text: headBuf{max: w.nodeText}}
	for i, a := range t.Attr {
		if i >= 64 {
			break
		}
		v, _ := rawCut(a.Value, w.nodeText)
		n.attrs = append(n.attrs, xml.Attr{Name: xml.Name{Space: a.Name.Space, Local: a.Name.Local}, Value: v})
	}
	return n
}

func (w *xmlWalker) text(n *xnode, b []byte) {
	if w.textUsed >= w.maxText {
		n.text.over = true
		return
	}
	if n.tail == nil && len(n.text.b)+len(b) > n.text.max {
		n.tail = &tailBuf{max: w.tailText}
		n.tail.write(n.text.b)
	}
	if n.tail != nil {
		n.tail.write(b)
	}
	before := len(n.text.b)
	n.text.write(b)
	w.textUsed += len(n.text.b) - before
}
