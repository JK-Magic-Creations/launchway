package pipeline

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseCaches reads a step's cache: a list of { name, paths, key, fallback,
// save }.
func (ps *parser) parseCaches(n *yaml.Node, at string) []*Cache {
	if n.Kind != yaml.SequenceNode {
		ps.add(n, at, "cache is a list, for example: "+cacheNamePathHint+". (An image step's cache is a different key: a registry that keeps build layers.)")
		return nil
	}
	if len(n.Content) == 0 {
		ps.add(n, at, "list at least one cache, or leave cache out")
		return nil
	}
	if len(n.Content) > MaxCachesPerStep {
		ps.add(n, at, "at most %d caches per step", MaxCachesPerStep)
	}
	var out []*Cache
	seen := map[string]bool{}
	for i, item := range n.Content {
		if i >= MaxCachesPerStep {
			break
		}
		where := at + "[" + strconv.Itoa(i) + "]"
		if item.Kind != yaml.MappingNode {
			ps.add(item, where, "a cache is a mapping with a name, paths and a key, for example: { name: pip, paths: [.cache/pip], key: { files: [requirements.txt] } }")
			continue
		}
		c := &Cache{Line: item.Line}
		before := len(ps.problems)
		idx := strconv.Itoa(i)
		// Values that name a loop's value are checked once the loop is
		// expanded; the others now.
		deferred := false
		scalar := func(v *yaml.Node, w, part string) string {
			before := 0
			if ps.cur != nil {
				before = len(ps.cur.eachRefs)
				ps.curField, ps.curName = cacheKeyDeferred, idx+"."+part
			}
			s := ps.scalar(v, w)
			if ps.cur != nil {
				ps.curField, ps.curName = "", ""
				if len(ps.cur.eachRefs) > before {
					deferred = true
				}
			}
			return s
		}
		var pathsNode, keyNode *yaml.Node
		ps.eachKey(item, where, []string{"name", "paths", "key", "fallback", "save"}, func(k, v *yaml.Node) {
			w := where + "." + k.Value
			switch k.Value {
			case "name":
				c.Name = scalar(v, w, "name")
			case "paths":
				pathsNode = v
				c.Paths = ps.cachePaths(v, w, "path.", scalar)
			case "key":
				keyNode = v
				c.Key = ps.cacheKey(v, w, idx, scalar)
			case "fallback":
				switch f := ps.str(v, w); f {
				case CacheFallbackNone:
				case CacheFallbackLatest:
					c.Fallback = f
				default:
					ps.add(v, w, "fallback is none (the default) or latest")
				}
			case "save":
				switch f := ps.str(v, w); f {
				case CacheSaveSuccess:
				case CacheSaveAlways, CacheSaveNever:
					c.Save = f
				default:
					ps.add(v, w, "save is success (the default), always or never")
				}
			}
		})
		if pathsNode == nil {
			ps.add(item, where+".paths", "say which directories or files to keep, such as paths: [.cache/pip]")
		}
		if keyNode == nil {
			ps.add(item, where+".key", "say what the key is made of, such as key: { files: [requirements.txt] }")
		}
		if pathsNode != nil && keyNode != nil && !deferred {
			// What the parts said already is not said again.
			if len(ps.problems) == before {
				for _, msg := range CacheProblems(c) {
					ps.add(item, where, "%s", msg)
				}
			}
		}
		if c.Name != "" && !deferred {
			if seen[c.Name] {
				ps.add(item, where+".name", "cache %s is listed twice", c.Name)
			}
			seen[c.Name] = true
		}
		out = append(out, c)
	}
	return out
}

// cachePaths reads the globs of a cache's paths, each of which may be a
// template parameter or a loop value.
func (ps *parser) cachePaths(v *yaml.Node, where, part string, scalar func(*yaml.Node, string, string) string) []string {
	items := []*yaml.Node{v}
	if v.Kind == yaml.SequenceNode {
		items = v.Content
	}
	if len(items) == 0 {
		ps.add(v, where, "list at least one file or glob, such as paths: [.cache/pip]")
		return nil
	}
	if len(items) > MaxCachePaths {
		ps.add(v, where, "at most %d paths per cache", MaxCachePaths)
		items = items[:MaxCachePaths]
	}
	var out []string
	for _, item := range items {
		if item.Kind == yaml.SequenceNode {
			ps.add(item, where, "expected a glob or a list of globs")
			continue
		}
		g := scalar(item, where, part+strconv.Itoa(len(out)))
		if g == "" {
			if item.Kind == yaml.ScalarNode {
				ps.add(item, where, "a path is empty; write a directory or glob such as .cache/pip")
			}
			continue
		}
		out = append(out, g)
	}
	return out
}

// cacheKey reads key: { files, text }.
func (ps *parser) cacheKey(v *yaml.Node, where, idx string, scalar func(*yaml.Node, string, string) string) CacheKey {
	var k CacheKey
	if v.Kind != yaml.MappingNode {
		ps.add(v, where, "key is a mapping: key: { files: [requirements.txt], text: v1 }")
		return k
	}
	ps.eachKey(v, where, []string{"files", "text"}, func(kk, vv *yaml.Node) {
		w := where + "." + kk.Value
		switch kk.Value {
		case "files":
			items := []*yaml.Node{vv}
			if vv.Kind == yaml.SequenceNode {
				items = vv.Content
			}
			if len(items) == 0 {
				ps.add(vv, w, "list at least one file or glob, such as files: [requirements.txt], or leave files out")
			}
			if len(items) > MaxCacheKeyFiles {
				ps.add(vv, w, "at most %d globs", MaxCacheKeyFiles)
				items = items[:MaxCacheKeyFiles]
			}
			for _, item := range items {
				if item.Kind != yaml.ScalarNode {
					ps.add(item, w, "a key file is a path or glob, such as requirements.txt")
					continue
				}
				g, msg := checkGlob(item.Value, "key files")
				if msg != "" {
					ps.add(item, w, "%s", msg)
					continue
				}
				k.Files = append(k.Files, g)
			}
		case "text":
			k.Text = scalar(vv, w, "text")
		}
	})
	return k
}

// cloneCaches copies caches so that a loop's members can each take their
// own values.
func cloneCaches(in []*Cache) []*Cache {
	if in == nil {
		return nil
	}
	out := make([]*Cache, len(in))
	for i, c := range in {
		cc := *c
		cc.Paths = append([]string(nil), c.Paths...)
		cc.Key.Files = append([]string(nil), c.Key.Files...)
		out[i] = &cc
	}
	return out
}

// setCacheValue puts a loop's value where a cache refers to it: part names
// the cache and the part of it, as "0.name", "0.text" or "0.path.2".
func setCacheValue(caches []*Cache, part, v string) {
	fields := strings.Split(part, ".")
	if len(fields) < 2 {
		return
	}
	i, err := strconv.Atoi(fields[0])
	if err != nil || i < 0 || i >= len(caches) {
		return
	}
	c := caches[i]
	switch fields[1] {
	case "name":
		c.Name = v
	case "text":
		c.Key.Text = v
	case "path":
		if len(fields) == 3 {
			if j, err := strconv.Atoi(fields[2]); err == nil && j >= 0 && j < len(c.Paths) {
				c.Paths[j] = v
			}
		}
	}
}
