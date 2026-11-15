// Package edit changes single values in YAML files, such as an image tag
// in a Deployment or a target revision in an Application, for deploy steps
// that commit to a GitOps repository.
//
// An edit changes only the text of the value it targets. Every other byte
// of the file stays as it was: comments, indentation, quoting, key order,
// blank lines, line endings and document separators. The YAML parser is
// used only to find values; the new text is written into the original
// bytes. After every edit the file is parsed again, and the edit is
// refused if anything other than the target reads back differently.
//
// Values written as blocks of text (| or >), over several lines, with an
// anchor (&name), an alias (*name) or an explicit tag (!!str) are refused
// rather than guessed at.
package edit

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Image sets the tag of every reference to an image in a YAML file and
// returns the new contents and how many references it found (including
// ones already at that tag, which are left byte-for-byte unchanged).
// Zero references is an error: the edit would silently do nothing.
//
// A reference is a single-line value (a map value or a list item, never a
// key) that reads as [registry/]repository[:tag][@digest]. It refers to
// image when its repository is image, in any registry or none; if image
// names a registry, the registry must match too. So for homelab/beacon,
// harbor.example.com/homelab/beacon:1 matches, but other/homelab/beacon:1
// and homelab/beacon-api:1 do not.
//
// A value with a tag or a digest is a reference wherever it is. A value
// with neither, such as homelab/beacon, is a reference only as the value
// of a key named image, and gets :tag added. Elsewhere it is left alone,
// because in a chart's values file (repository: homelab/beacon with a
// separate tag key) adding a tag would be wrong; set such a tag with Set.
//
// A reference pinned by digest is refused: Launchway sets tags only.
func Image(data []byte, image, tag string) (out []byte, matches int, err error) {
	if err := CheckImage(image); err != nil {
		return nil, 0, err
	}
	if err := CheckTag(tag); err != nil {
		return nil, 0, err
	}
	want, _ := parseRef(image)
	docs, err := readDocs(data)
	if err != nil {
		return nil, 0, err
	}
	t := newText(data)
	var changes []change
	expect := map[*yaml.Node]string{}

	// check looks at one value, whose key is key ("" for a list item).
	check := func(n *yaml.Node, key string) error {
		if n.Kind != yaml.ScalarNode || n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return nil
		}
		v := strings.TrimSpace(n.Value)
		r, ok := parseRef(v)
		if !ok || !r.refersTo(want) {
			return nil
		}
		if r.tag == "" && r.digest == "" && key != "image" {
			return nil
		}
		if n.Style&yaml.TaggedStyle == 0 && n.ShortTag() != "!!str" {
			return nil
		}
		if why := refusal(n); why != "" {
			return fmt.Errorf("line %d: the reference to image %s %s", n.Line, image, why)
		}
		start, end, err := t.span(n)
		if errors.Is(err, errMultiLine) {
			return nil // not looked at, as documented
		}
		if err != nil {
			return fmt.Errorf("line %d: the reference to image %s %v", n.Line, image, err)
		}
		if r.digest != "" {
			return fmt.Errorf("line %d: image %s is pinned by digest (@%s); Launchway sets tags only", n.Line, r.name(), shortDigest(r.digest))
		}
		matches++
		lead := n.Value[:strings.Index(n.Value, v)]
		trail := n.Value[len(lead)+len(v):]
		nv := lead + r.name() + ":" + tag + trail
		if nv == n.Value {
			return nil
		}
		changes = append(changes, change{start: start, end: end, text: render(n.Style, nv)})
		expect[n] = nv
		return nil
	}

	var walk func(n *yaml.Node, key string) error
	walk = func(n *yaml.Node, key string) error {
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				if c.Kind != yaml.ScalarNode { // a whole document that is one value is not looked at
					if err := walk(c, ""); err != nil {
						return err
					}
				}
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := ""
				if n.Content[i].Kind == yaml.ScalarNode {
					k = n.Content[i].Value
				}
				if err := walk(n.Content[i+1], k); err != nil {
					return err
				}
			}
		case yaml.SequenceNode:
			for _, c := range n.Content {
				if err := walk(c, ""); err != nil {
					return err
				}
			}
		case yaml.ScalarNode:
			return check(n, key)
		}
		return nil // an alias is looked at where its anchor is
	}
	for _, d := range docs {
		if err := walk(d, ""); err != nil {
			return nil, 0, err
		}
	}
	if matches == 0 {
		return nil, 0, fmt.Errorf("no reference to image %s was found, so there is nothing to change; "+
			"for a chart's values file with separate repository and tag keys, set the tag with a path such as image.tag", image)
	}
	if len(changes) == 0 {
		return data, matches, nil
	}
	out = apply(data, changes)
	if err := verify(docs, out, expect); err != nil {
		return nil, 0, err
	}
	return out, matches, nil
}

// Set sets the scalar at a YAML path, such as image.tag or
// spec.sources[0].targetRevision, to value. Exactly one document of the
// file must have the path; zero or more than one is an error.
//
// The target must already exist and be a single value; Set never adds
// keys. An empty value (key: with nothing after it) may be set. The
// value's quoting is kept. A plain value is written plain only if it reads
// back as the same string; otherwise it is double-quoted, so 1.20, true or
// 0123 stay strings. The path may not pass through anchors or aliases.
func Set(data []byte, path, value string) (out []byte, err error) {
	p, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	ps := p.String()
	if strings.ContainsAny(value, "\r\n") || strings.ContainsRune(value, '\u0085') ||
		strings.ContainsRune(value, 0x2028) || strings.ContainsRune(value, 0x2029) {
		return nil, fmt.Errorf("%s: the value has a line break; Launchway sets single-line values only", ps)
	}
	if !utf8.ValidString(value) {
		return nil, fmt.Errorf("%s: the value is not valid UTF-8 text", ps)
	}
	docs, err := readDocs(data)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("%s: the file is empty", ps)
	}

	var hits []found
	var misses []string
	for _, d := range docs {
		h, miss, err := find(d, p)
		if err != nil {
			return nil, err
		}
		if miss != "" {
			misses = append(misses, miss)
			continue
		}
		hits = append(hits, h)
	}
	switch {
	case len(hits) == 0 && len(docs) == 1:
		return nil, fmt.Errorf("%s: %s", ps, misses[0])
	case len(hits) == 0:
		for i := range misses {
			misses[i] = fmt.Sprintf("document %d: %s", i+1, misses[i])
		}
		return nil, fmt.Errorf("%s: none of the %d documents in the file has this path (%s)", ps, len(docs), strings.Join(misses, "; "))
	case len(hits) > 1:
		lines := make([]string, len(hits))
		for i, h := range hits {
			lines[i] = fmt.Sprint(h.node.Line)
		}
		return nil, fmt.Errorf("%s: the path is in %d documents (lines %s); Launchway changes exactly one, so nothing was changed",
			ps, len(hits), strings.Join(lines, ", "))
	}

	h := hits[0]
	n := h.node
	if why := refusal(n); why != "" {
		return nil, fmt.Errorf("%s: this %s (line %d)", ps, why, n.Line)
	}
	if n.Value == value && n.ShortTag() == "!!str" {
		return data, nil
	}
	t := newText(data)
	var c change
	if n.Value == "" && n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) == 0 {
		at, ok := t.emptyValueAt(h.key)
		if !ok {
			return nil, fmt.Errorf("%s: there is no value written here to replace (line %d); write a placeholder such as \"\" first", ps, n.Line)
		}
		c = change{start: at, end: at, text: " " + render(0, value)}
	} else {
		start, end, err := t.span(n)
		if err != nil {
			return nil, fmt.Errorf("%s: this value %v (line %d)", ps, err, n.Line)
		}
		c = change{start: start, end: end, text: render(n.Style, value)}
	}
	out = apply(data, []change{c})
	if err := verify(docs, out, map[*yaml.Node]string{n: value}); err != nil {
		return nil, err
	}
	return out, nil
}

// found is where a path ends: the node, and its key when it is a map value.
type found struct {
	node, key *yaml.Node
}

// find follows a path in one document. miss says why the document does
// not have the path; err is for a path that cannot be followed safely.
func find(doc *yaml.Node, p path) (h found, miss string, err error) {
	if len(doc.Content) == 0 {
		return found{}, "the document is empty", nil
	}
	cur := doc.Content[0]
	if cur.Kind == yaml.ScalarNode && cur.ShortTag() == "!!null" && cur.Value == "" {
		return found{}, "the document is empty", nil
	}
	for i, st := range p {
		at := p[:i].String()
		where := "under " + at
		if i == 0 {
			where = "at the top of the document"
		}
		name := at
		if i == 0 {
			name = "the document"
		}
		switch {
		case cur.Kind == yaml.AliasNode:
			return found{}, "", fmt.Errorf("%s: %s is an alias (*%s); Launchway does not edit through aliases (line %d)", p, name, cur.Value, cur.Line)
		case cur.Anchor != "":
			return found{}, "", fmt.Errorf("%s: %s has an anchor (&%s), so changing a value inside it would also change every *%s; Launchway does not edit inside anchors (line %d)",
				p, name, cur.Anchor, cur.Anchor, cur.Line)
		}
		if st.isIndex {
			if cur.Kind != yaml.SequenceNode {
				return found{}, fmt.Sprintf("%s is %s, not a list, so it has no item [%d] (line %d)", at, kindName(cur), st.index, cur.Line), nil
			}
			if st.index >= len(cur.Content) {
				return found{}, fmt.Sprintf("there is no item [%d] %s, which has %s (line %d)", st.index, where, items(len(cur.Content)), cur.Line), nil
			}
			cur, h.key = cur.Content[st.index], nil
			continue
		}
		if cur.Kind != yaml.MappingNode {
			if i == 0 {
				return found{}, fmt.Sprintf("the document is %s, not a map, so it has no key %q (line %d)", kindName(cur), st.key, cur.Line), nil
			}
			return found{}, fmt.Sprintf("%s is %s, not a map, so it has no key %q (line %d)", at, kindName(cur), st.key, cur.Line), nil
		}
		var next, key *yaml.Node
		merge := false
		for j := 0; j+1 < len(cur.Content); j += 2 {
			k := cur.Content[j]
			if k.Kind != yaml.ScalarNode {
				continue
			}
			if k.Value == "<<" && k.Style == 0 {
				merge = true
			}
			if k.Value == st.key {
				key, next = k, cur.Content[j+1]
				break
			}
		}
		if next == nil {
			msg := fmt.Sprintf("there is no key %q %s (line %d)", st.key, where, cur.Line)
			if merge {
				msg += "; keys brought in with << are not followed"
			}
			return found{}, msg, nil
		}
		cur, h.key = next, key
	}
	h.node = cur
	return h, "", nil
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a map"
	case yaml.SequenceNode:
		return "a list"
	case yaml.AliasNode:
		return "an alias"
	}
	return "a single value"
}

func items(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}
