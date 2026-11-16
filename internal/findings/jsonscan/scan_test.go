package jsonscan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestValueMatchesEncodingJSON(t *testing.T) {
	for _, in := range []string{
		`0`, `-0`, `1.5e10`, `-1E-2`, `"a\"b\\c\/\b\f\n\r\t\u00e9"`, `true`, `false`, `null`,
		`[]`, `{}`, `[1,[2,[3]],{"a":{"b":[]}}]`, ` { "a" : 1 , "b" : [ true , null ] } `,
		"\"\xff\xfe invalid utf-8 is allowed\"",
		// Not JSON:
		``, ` `, `01`, `1.`, `.5`, `1e`, `+1`, `-`, `tru`, `nul`, `[1,]`, `{"a":1,}`, `{"a"}`, `{a:1}`, `[1 2]`,
		`"abc`, `"\x"`, `"\u12"`, "\"a\tb\"", `{"a":1}}`, `[`, `{"a":`, `{"a"`, `{"a`, `[1,`, `"\`,
	} {
		s := New([]byte(in), nil)
		v, info, err := s.Value()
		ok := err == nil && !s.Rest()
		if want := json.Valid([]byte(in)); ok != want {
			t.Errorf("%q: scanner says valid=%v (err %v), encoding/json says %v", in, ok, err, want)
		}
		if ok && strings.TrimSpace(in) != string(v) {
			t.Errorf("%q: value = %q", in, v)
		}
		if err == nil && info.Depth < 0 {
			t.Errorf("%q: depth %d", in, info.Depth)
		}
	}
}

func TestDepthAndLongestString(t *testing.T) {
	for in, want := range map[string]Info{
		`"abc"`:                  {0, 3},
		`[]`:                     {1, 0},
		`[[],[[]]]`:              {3, 0},
		`{"key":{"k":"vvvvvv"}}`: {2, 6},
		`{"a\"b":1}`:             {1, 4},
	} {
		_, info, err := New([]byte(in), nil).Value()
		if err != nil || info != want {
			t.Errorf("%s: info %+v err %v, want %+v", in, info, err, want)
		}
	}
	deep := strings.Repeat("[", 200000) + strings.Repeat("]", 200000)
	_, info, err := New([]byte(deep), nil).Value()
	if err != nil || info.Depth != 200000 {
		t.Errorf("deep: depth %d, err %v", info.Depth, err)
	}
}

func TestErrors(t *testing.T) {
	_, _, err := New([]byte(`[1,2`), nil).Value()
	var se *SyntaxError
	if !errors.As(err, &se) || !se.EOF || se.Offset != 4 {
		t.Errorf("err = %#v", err)
	}
	_, _, err = New([]byte("[1,\n x]"), nil).Value()
	if !errors.As(err, &se) || se.EOF || se.Offset != 5 || se.Msg != "expected a JSON value, found 'x'" {
		t.Errorf("err = %#v", err)
	}
	if line := New([]byte("a\nb\nc"), nil).Line(4); line != 3 {
		t.Errorf("line = %d", line)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	big := []byte("[" + strings.Repeat("1,", 100000) + "1]")
	_, _, err = New(big, NewDeadline(ctx, time.Minute)).Value()
	if !errors.Is(err, ErrStopped) {
		t.Errorf("err = %v", err)
	}
}

func TestObjectAndArray(t *testing.T) {
	s := New([]byte(`{"a":1,"b\u0021":[1,2,3],"c":{"d":"e"},"`+strings.Repeat("k", 300)+`":0}`), nil)
	var keys []string
	err := s.Object(func(key string) error {
		keys = append(keys, key)
		if key == "b!" {
			n := 0
			return s.Array(func() error { n++; return s.Skip() })
		}
		return nil // values not read here are skipped by Object
	})
	if err != nil || strings.Join(keys, ",") != "a,b!,c," {
		t.Errorf("keys %q, err %v", keys, err)
	}
	stop := errors.New("stop")
	if err := New([]byte(`{"a":1}`), nil).Object(func(string) error { return stop }); err != stop {
		t.Errorf("err = %v", err)
	}
	if err := New([]byte(`[1]`), nil).Object(func(string) error { return nil }); err == nil {
		t.Error("an array was read as an object")
	}
}

func TestValid(t *testing.T) {
	for in, want := range map[string]bool{
		`{"a":1,"b":2}`:             true,
		`{"a":1,"a":2}`:             false,
		`{"a":1,"\u0061":2}`:        false,
		`[{"x":{"y":1,"y":1}}]`:     false,
		"\"\xff\"":                  true,
		`{"a":{"b":1},"c":{"b":1}}`: true,
	} {
		if got := Valid([]byte(in)); got != want {
			t.Errorf("Valid(%s) = %v", in, got)
		}
	}
}

func TestShrink(t *testing.T) {
	in := `{"short":"ok","long":"` + strings.Repeat("ab€\\n\\u00e9\\\"", 1000) + `","k` + strings.Repeat("x", 100) + `":[1,"` + strings.Repeat("z", 50) + `"]}`
	v, info, err := New([]byte(in), nil).Value()
	if err != nil {
		t.Fatal(err)
	}
	for limit := 0; limit < 60; limit++ {
		out := Shrink(v, info, limit)
		if !json.Valid(out) {
			t.Fatalf("limit %d: not valid JSON: %s", limit, out)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		for k, x := range m {
			if s, ok := x.(string); ok && len(s) > limit+len(CutMark)+3 {
				t.Errorf("limit %d: %s is %d bytes", limit, k, len(s))
			}
			if !utf8.ValidString(k) {
				t.Errorf("limit %d: key %q", limit, k)
			}
		}
	}
	if out := Shrink(v, info, 1<<20); !bytes.Equal(out, v) {
		t.Error("nothing to cut, but the value changed")
	}
}

func TestTypes(t *testing.T) {
	var v struct {
		S  Str
		L  LooseStr
		I  Int
		N  Num
		T  Strings
		F  RawFirst
		I2 Int
	}
	in := `{"S":12,"L":{"x":1},"I":"42","N":"9.8","T":["a",1,{"b":2},"c"],"F":[{"a":1},{"b":2}],"I2":1e400}`
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatal(err)
	}
	if v.S != "12" || v.L != "" || v.I != (Int{42, true, true}) || v.N != (Num{9.8, true}) || strings.Join(v.T.List, ",") != "a,1,c" || string(v.F.V) != `{"a":1}` || v.I2.OK || !v.I2.Set {
		t.Errorf("decoded %+v", v)
	}
	for _, bad := range []string{`{"S":true}`, `{"S":[]}`, `{"I":{}}`, `{"I":false}`} {
		if err := json.Unmarshal([]byte(bad), &v); err == nil {
			t.Errorf("%s decoded without an error", bad)
		}
	}
}

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                        "plain",
		"a\x00b\x07c":                  "abc",
		"red \x1b[31mtext\x1b[0m":      "red text",
		"osc \x1b]8;;http://x\x07link": "osc link",
		"cr\r\nlf\rcr":                 "cr\nlf\ncr",
		"tab\there":                    "tab here",
		"bad \xff\xfe":                 "bad ��",
		"c1 \u0085 \u009b":             "c1  ",
		"bidi ‮evil‬ ⁦x":               "bidi evil x",
		"emoji 😀 and accents é":        "emoji 😀 and accents é",
		"\x1b":                         "",
		"\x1b[":                        "[",
	} {
		if got := CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Block("\n\n  \n  first\n\nsecond  \n\n", 100); got != "first\n\nsecond" {
		t.Errorf("Block = %q", got)
	}
	if got := Line(" a\nb\tc ", 100); got != "a b c" {
		t.Errorf("Line = %q", got)
	}
	s := strings.Repeat("ab€", 100)
	for n := 0; n < 40; n++ {
		c := Cut(s, n)
		if len(c) > n || !utf8.ValidString(c) || (n > 3 && !strings.HasSuffix(c, CutMark)) {
			t.Errorf("Cut(%d) = %q", n, c)
		}
	}
}

func FuzzValue(f *testing.F) {
	for _, s := range []string{`{"a":[1,2,{"b":null}]}`, `"\u00e9"`, `[`, `-0.5e+3`, "\"\xff\"", `{"a":1,"a":2}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s := New(data, nil)
		v, info, err := s.Value()
		ok := err == nil && !s.Rest()
		if want := json.Valid(data); ok != want {
			t.Fatalf("scanner valid=%v (err %v), encoding/json valid=%v", ok, err, want)
		}
		if err != nil {
			return
		}
		out := Shrink(v, info, 3)
		if !json.Valid(out) {
			t.Fatalf("Shrink made invalid JSON: %q", out)
		}
		if bytes.Equal(out, v) != (info.LongestString <= 3) {
			t.Fatalf("Shrink copied when it need not, or kept a long string")
		}
	})
}
