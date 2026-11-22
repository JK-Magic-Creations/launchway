package importer

import (
	"bytes"
	"strings"

	"gopkg.in/yaml.v3"
)

// A tiny builder over the YAML library's nodes, so the writer produces
// well-formed text whatever the values hold (quotes, colons, newlines).

func ystr(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if strings.Contains(s, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

func ybool(b bool) *yaml.Node {
	v := "false"
	if b {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

func yint(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: itoa(i)}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func ymap() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }
func yseq(flow bool, items ...*yaml.Node) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
	if flow {
		n.Style = yaml.FlowStyle
	}
	return n
}

func yadd(m *yaml.Node, key string, v *yaml.Node) *yaml.Node {
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return v
}

func ystrs(list []string) *yaml.Node {
	items := make([]*yaml.Node, len(list))
	for i, s := range list {
		items[i] = ystr(s)
	}
	return yseq(!anyMultiline(list), items...)
}

func anyMultiline(l []string) bool {
	for _, s := range l {
		if strings.Contains(s, "\n") || len(s) > 60 {
			return true
		}
	}
	return false
}

func ykv(kvs []KV) *yaml.Node {
	m := ymap()
	for _, kv := range kvs {
		yadd(m, kv.K, ystr(kv.V))
	}
	if len(kvs) <= 3 {
		m.Style = yaml.FlowStyle
		for _, kv := range kvs {
			if strings.ContainsAny(kv.V, "\n") {
				m.Style = 0
			}
		}
	}
	return m
}

func yencode(root *yaml.Node) (string, error) {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(root); err != nil {
		return "", err
	}
	_ = e.Close()
	return b.String(), nil
}
