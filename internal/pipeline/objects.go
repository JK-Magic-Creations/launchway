package pipeline

// Parameters of type objects: a list of records, such as the images of an application.
//
//	params:
//	  apps: { type: objects, fields: [repository, dockerfile, context] }
//	steps:
//	  build:
//	    uses: build
//	    for_each: { app: { param: apps } }          # one build for each record
//	    with:
//	      image_repository: { each: app, field: repository }
//	      dockerfile: { each: app, field: dockerfile, default: Dockerfile }
//
// and, where the template is used:
//
//	with:
//	  apps:
//	    - { name: backend, repository: shop/backend, dockerfile: backend/Dockerfile }
//	    - { name: frontend, repository: shop/frontend }
//
// Every record has a name, which is its value in a loop (it makes the member's ID, so it is
// written as a loop value is) and a text for each field. A value is kept as a JSON array of
// records, with the fields in alphabetical order, so that two ways of writing the same list
// are the same value.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limits on a parameter of type objects.
const (
	MaxObjects         = 32
	MaxObjectFields    = 20
	MaxObjectValueLen  = 512
	paramTypeObjects   = "objects"
	objectNameField    = "name"
	objectFieldPattern = `^[a-z][a-z0-9_]{0,39}$`
)

var fieldNameRe = regexp.MustCompile(objectFieldPattern)

// canonParam writes a value of a parameter in the form values of its type are compared and passed on in.
func canonParam(p *Param, v string) string {
	switch p.Type {
	case paramTypeList:
		return canonList(v)
	case paramTypeObjects:
		return canonObjects(v)
	}
	return v
}

// Object is one record of a list of objects: its fields, name among them.
type Object map[string]string

// ParseObjects reads a list of objects from its text, a JSON array of mappings whose values are
// text, numbers or booleans. It returns why it is not one, in words.
func ParseObjects(raw string) ([]Object, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return []Object{}, ""
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		return nil, "a list of objects is a JSON array of records, such as [{\"name\":\"api\",\"dockerfile\":\"Dockerfile\"}]; write it in YAML as a list of mappings"
	}
	if len(rows) > MaxObjects {
		return nil, fmt.Sprintf("a list of objects has at most %d records", MaxObjects)
	}
	out := make([]Object, 0, len(rows))
	names := map[string]bool{}
	for i, row := range rows {
		if len(row) > MaxObjectFields {
			return nil, fmt.Sprintf("record %d has more than %d fields", i+1, MaxObjectFields)
		}
		o := Object{}
		for k, v := range row {
			var text string
			switch x := v.(type) {
			case string:
				text = x
			case bool:
				text = strconv.FormatBool(x)
			case float64:
				text = strconv.FormatFloat(x, 'f', -1, 64)
			default:
				return nil, fmt.Sprintf("record %d: the field %s is not text, a number or true or false", i+1, k)
			}
			if len(text) > MaxObjectValueLen {
				return nil, fmt.Sprintf("record %d: the field %s is longer than %d bytes", i+1, k, MaxObjectValueLen)
			}
			o[k] = text
		}
		name := o[objectNameField]
		switch {
		case name == "":
			return nil, fmt.Sprintf("record %d has no name: every record is named, name: api, which is what a loop calls it", i+1)
		case names[name]:
			return nil, fmt.Sprintf("two records are named %q", name)
		}
		names[name] = true
		out = append(out, o)
	}
	return out, ""
}

// canonObjects writes a list of objects canonically: a JSON array, fields in alphabetical order.
func canonObjects(v string) string {
	items, msg := ParseObjects(v)
	if msg != "" {
		return v
	}
	return objectsJSON(items)
}

func objectsJSON(items []Object) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, o := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		keys := make([]string, 0, len(o))
		for k := range o {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for j, k := range keys {
			if j > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			vb, _ := json.Marshal(o[k])
			b.Write(kb)
			b.WriteByte(':')
			b.Write(vb)
		}
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.String()
}

// ObjectItems reads a canonical list of objects; nil if it is not one.
func ObjectItems(canon string) []Object {
	items, msg := ParseObjects(canon)
	if msg != "" {
		return nil
	}
	return items
}

// checkObjects says what is wrong with a value of a parameter of type objects: the shape, and, if the
// parameter lists its fields, a field it does not have.
func (p *Param) checkObjects(v string) string {
	items, msg := ParseObjects(v)
	if msg != "" {
		return fmt.Sprintf("%s is a list of objects: %s", p.Name, msg)
	}
	if len(p.Fields) == 0 {
		return ""
	}
	for _, o := range items {
		for k := range o {
			if k != objectNameField && !slices.Contains(p.Fields, k) {
				return fmt.Sprintf("%s: the record %q has a field %q that the template does not take; its fields are name, %s", p.Name, o[objectNameField], k, strings.Join(p.Fields, ", "))
			}
		}
	}
	return ""
}

// objectsNode reads a YAML list of mappings written for a parameter of type objects (a use's with, or
// a default) as the canonical text of the value. Every field is a single value.
func (ps *parser) objectsNode(n *yaml.Node, at string) string {
	rows := make([]map[string]any, 0, len(n.Content))
	for i, c := range n.Content {
		where := fmt.Sprintf("%s[%d]", at, i)
		if c.Kind != yaml.MappingNode {
			ps.add(c, where, "a list of objects holds mappings, such as { name: api, dockerfile: Dockerfile }")
			continue
		}
		row := map[string]any{}
		for j := 0; j+1 < len(c.Content); j += 2 {
			k, v := c.Content[j], c.Content[j+1]
			if v.Kind != yaml.ScalarNode {
				ps.add(v, where+"."+k.Value, "a field of a record is a single value")
				continue
			}
			if _, dup := row[k.Value]; dup {
				ps.add(k, where+"."+k.Value, "%q is written twice", k.Value)
				continue
			}
			row[k.Value] = v.Value
		}
		rows = append(rows, row)
	}
	b, _ := json.Marshal(rows)
	return canonObjects(string(b))
}

// objectEach is the text of one field of a record of a loop, or why there is none.
func objectEach(l LoopList, member, field, def string, hasDefault bool) (string, bool) {
	if field == objectNameField {
		return member, true
	}
	if v, ok := l.Objects[member][field]; ok {
		return v, true
	}
	return def, hasDefault
}

// ---- repeated entries ----

// eachEntry calls fn for every entry of a list of a template (a deploy step's changes and sync resources, a
// verify step's metrics and logs, an image step's tags and secrets, an ai step's inputs) that stays: an entry
// with an if that is false is left out, and an entry with for_each is written once for each value of the list
// it names, each with the values of its record:
//
//	changes:
//	  - for_each: { app: { param: apps } }
//	    file: { each: app, field: manifest }
//	    image: { each: app, field: repository }
//	    tag: { output: { each: app, format: "build[app=%s].tag" } }
//
// at is the path of the entry, as the list's reader would write it.
func (ps *parser) eachEntry(list []*yaml.Node, where string, fn func(item *yaml.Node, at string)) {
	for _, e := range ps.expandEntries(list, where) {
		fn(e.n, e.at)
	}
}

// entryAt is an entry that stays, and its path.
type entryAt struct {
	n  *yaml.Node
	at string
}

// expandEntries is the entries of a list that stay, with their paths (see eachEntry).
func (ps *parser) expandEntries(list []*yaml.Node, where string) []entryAt {
	var out []entryAt
	for i, c := range list {
		at := fmt.Sprintf("%s[%d]", where, i)
		for _, m := range ps.entries(c, at) {
			out = append(out, entryAt{m, at})
		}
	}
	return out
}

// entries is the entries one written entry stands for: none, itself, or one for each of its loop.
func (ps *parser) entries(n *yaml.Node, at string) []*yaml.Node {
	if ps.tmpl == nil || n.Kind != yaml.MappingNode || !hasKey(n, "for_each") {
		if m, keep := ps.entry(n, at); keep {
			return []*yaml.Node{m}
		}
		return nil
	}
	rest := &yaml.Node{Kind: n.Kind, Tag: n.Tag, Line: n.Line, Column: n.Column}
	var spec *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "for_each" {
			spec = n.Content[i+1]
			continue
		}
		rest.Content = append(rest.Content, n.Content[i], n.Content[i+1])
	}
	key, names, recs, ok := ps.entryLoop(spec, at+".for_each")
	if !ok {
		return nil
	}
	var out []*yaml.Node
	for _, name := range names {
		cp := ps.substituteEach(rest, at, key, name, recs[name])
		if m, keep := ps.entry(cp, at); keep {
			out = append(out, m)
		}
	}
	return out
}

// entryLoop reads the for_each of an entry: one key and the list it takes its values from, a list or a
// list of objects, a parameter of the template or written in place.
func (ps *parser) entryLoop(n *yaml.Node, at string) (key string, names []string, recs map[string]Object, ok bool) {
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		ps.add(n, at, "for_each on an entry has one key and one list, such as for_each: { app: { param: apps } }")
		return
	}
	k, v := n.Content[0], n.Content[1]
	key = k.Value
	if !paramNameRe.MatchString(key) {
		ps.add(k, at, "%q is not a valid name: use lower-case letters, digits, - and _, starting with a letter (at most 40)", key)
		return
	}
	switch {
	case v.Kind == yaml.SequenceNode:
		for _, c := range v.Content {
			if c.Kind != yaml.ScalarNode {
				ps.add(c, at, "a list holds single values")
				return
			}
			names = append(names, c.Value)
		}
	case v.Kind == yaml.MappingNode && hasKey(v, "param"):
		var name string
		ps.eachKey(v, at+"."+key, []string{"param"}, func(_, v2 *yaml.Node) { name = ps.str(v2, at+"."+key+".param") })
		p := ps.tmpl.params[name]
		switch {
		case p == nil:
			ps.add(v, at+"."+key+".param", "%s", ps.unknownParam(name))
			return
		case p.Type == paramTypeObjects:
			recs = map[string]Object{}
			for _, o := range ObjectItems(ps.tmpl.values[name]) {
				names = append(names, o[objectNameField])
				recs[o[objectNameField]] = o
			}
		case p.Type == paramTypeList:
			names = ListItems(ps.tmpl.values[name])
		default:
			ps.add(v, at+"."+key+".param", "%s is a %s; for_each needs a parameter of type list or objects", name, p.Type)
			return
		}
	default:
		ps.add(v, at+"."+key, "write a list, such as [a, b], or { param: name } for a parameter of type list or objects")
		return
	}
	if len(names) > DefaultMaxLoopMembers {
		ps.add(n, at, "the list has %d values; at most %d", len(names), DefaultMaxLoopMembers)
		return
	}
	return key, names, recs, true
}

// substituteEach copies a node, writing for each { each: key, field: f } in it the member's value.
func (ps *parser) substituteEach(n *yaml.Node, at, key, member string, rec Object) *yaml.Node {
	if n.Kind == yaml.MappingNode && hasKey(n, "each") {
		var ref eachRef
		var formatNode *yaml.Node
		ps.eachKey(n, at, []string{"each", "format", "field", "default"}, func(k, v *yaml.Node) {
			switch k.Value {
			case "each":
				ref.key = ps.str(v, at+".each")
			case "field":
				ref.attr = ps.str(v, at+".field")
			case "default":
				ref.def, ref.hasDef = ps.str(v, at+".default"), true
			default:
				ref.format, formatNode = ps.str(v, at+".format"), v
			}
		})
		if ref.key != key {
			ps.add(n, at, "there is no list called %q under for_each; it has %s", clipText(ref.key, 40), key)
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "", Line: n.Line, Column: n.Column}
		}
		if formatNode != nil && (strings.Count(ref.format, "%s") != 1 || strings.Count(ref.format, "%") != 1) {
			ps.add(formatNode, at+".format", "a format has exactly one %%s, where the value goes, and no other %%")
			ref.format = ""
		}
		v := member
		if ref.attr != "" {
			var found bool
			if v, found = objectEach(LoopList{Objects: map[string]Object{member: rec}}, member, ref.attr, ref.def, ref.hasDef); !found {
				ps.add(n, at, "{ each: %s, field: %s }: the record %q has no field %s; give it one, or write default: in the reference", key, ref.attr, member, ref.attr)
			}
		}
		if ref.format != "" {
			v = strings.Replace(ref.format, "%s", v, 1)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Line: n.Line, Column: n.Column}
	}
	c := *n
	c.Content = nil
	for _, x := range n.Content {
		c.Content = append(c.Content, ps.substituteEach(x, at, key, member, rec))
	}
	return &c
}

// parseFieldNames reads the fields a parameter of type objects lists.
func (ps *parser) parseFieldNames(n *yaml.Node, at string) []string {
	fields := ps.strList(n, at)
	for _, f := range fields {
		if !fieldNameRe.MatchString(f) || f == objectNameField {
			ps.add(n, at, "%q is not a field name: use lower-case letters, digits and _, starting with a letter; name is every record's and is not listed", clipText(f, 40))
		}
	}
	return fields
}

// checkParamKinds checks the keys of a parameter against its type: fields are for objects, options for strings
// and numbers.
func (ps *parser) checkParamKinds(p *Param, at string, name, optNode *yaml.Node) {
	if len(p.Fields) > 0 && p.Type != paramTypeObjects {
		ps.add(name, at+".fields", "fields are for a parameter of type objects; %s is a %s", p.Name, p.Type)
	}
	if optNode != nil && (p.Type == paramTypeBoolean || p.Type == paramTypeList || p.Type == paramTypeObjects) {
		ps.add(optNode, at+".options", "options are for strings and numbers")
	}
}
