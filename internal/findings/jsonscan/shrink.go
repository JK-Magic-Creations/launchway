package jsonscan

import "unicode/utf8"

// Shrink returns a copy of the JSON value v (already read by Value) in which
// every string longer than limit bytes is cut to at most limit bytes and
// ends with "…". Decoding the copy then costs memory in proportion to what
// is kept rather than to what the file holds. Escapes and characters are
// never split. When no string is that long, v itself is returned.
func Shrink(v []byte, info Info, limit int) []byte {
	if info.LongestString <= limit {
		return v
	}
	out := make([]byte, 0, min(len(v), 1<<20))
	for i := 0; i < len(v); {
		c := v[i]
		if c != '"' {
			out = append(out, c)
			i++
			continue
		}
		// A string: copy up to limit bytes of its content.
		out = append(out, '"')
		i++
		kept, cut := 0, false
		for i < len(v) && v[i] != '"' {
			n := 1
			if v[i] == '\\' {
				n = 2
				if i+1 < len(v) && v[i+1] == 'u' {
					n = 6
				}
			} else if v[i] >= utf8.RuneSelf {
				_, n = utf8.DecodeRune(v[i:])
			}
			n = min(n, len(v)-i)
			if !cut && kept+n <= limit {
				out = append(out, v[i:i+n]...)
				kept += n
			} else {
				cut = true
			}
			i += n
		}
		if cut {
			out = append(out, CutMark...)
		}
		out = append(out, '"')
		i++
	}
	return out
}
