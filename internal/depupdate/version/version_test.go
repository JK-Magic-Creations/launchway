package version

import "testing"

func TestSemverCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0}, {"1.2.3", "1.10.0", -1}, {"v2.0.0", "v1.99.99", 1},
		{"1.0.0-rc.1", "1.0.0", -1}, {"1.0.0-alpha", "1.0.0-alpha.1", -1}, {"1.0.0-alpha.2", "1.0.0-alpha.10", -1},
		{"1.0.0-1", "1.0.0-alpha", -1}, {"1.2", "1.2.0", 0}, {"v0.0.0-20210101120000-abcdef123456", "v0.1.0", -1},
	}
	for _, c := range cases {
		a, ok1 := ParseSemver(c.a)
		b, ok2 := ParseSemver(c.b)
		if !ok1 || !ok2 {
			t.Fatalf("parse %q %q", c.a, c.b)
		}
		if got := a.Compare(b); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"", "x", "1.2.3.4", "1.a.3", "latest"} {
		if _, ok := ParseSemver(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestSemverFlags(t *testing.T) {
	v, _ := ParseSemver("v0.0.0-20210101120000-abcdef123456")
	if !v.IsPseudo() || !v.IsPre() {
		t.Error("pseudo-version not recognized")
	}
	v, _ = ParseSemver("v1.2.4-0.20210101120000-abcdef123456")
	if !v.IsPseudo() {
		t.Error("second pseudo form")
	}
	v, _ = ParseSemver("v1.2.4-rc.1")
	if v.IsPseudo() || !v.IsPre() {
		t.Error("rc is pre, not pseudo")
	}
}

func TestKindOf(t *testing.T) {
	cases := []struct {
		a, b string
		want Kind
	}{{"1.2.3", "1.2.4", KindPatch}, {"1.2.3", "1.3.0", KindMinor}, {"1.2.3", "2.0.0", KindMajor}, {"1.2.3", "1.2.3", KindNone}, {"2.0.0", "1.0.0", KindNone}}
	for _, c := range cases {
		a, _ := ParseSemver(c.a)
		b, _ := ParseSemver(c.b)
		if got := KindOf(a, b); got != c.want {
			t.Errorf("%s->%s = %s want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestRanges(t *testing.T) {
	cases := []struct {
		r, v string
		in   bool
	}{
		{"^1.2.3", "1.9.0", true}, {"^1.2.3", "2.0.0", false}, {"^1.2.3", "1.2.2", false},
		{"^0.2.3", "0.2.9", true}, {"^0.2.3", "0.3.0", false}, {"^0.0.3", "0.0.4", false},
		{"~1.2.3", "1.2.9", true}, {"~1.2.3", "1.3.0", false}, {"~1", "1.9.9", true},
		{">=1.0.0 <2.0.0", "1.5.0", true}, {">=1.0.0 <2.0.0", "2.0.0", false},
		{"1.x", "1.7.1", true}, {"1.x", "2.0.0", false}, {"1.2.x", "1.2.9", true}, {"1.2.x", "1.3.0", false},
		{"1.2.3 - 2.0.0", "1.9.0", true}, {"1.2.3 - 2.0.0", "2.0.1", false},
		{"^1.0.0 || ^3.0.0", "3.1.0", true}, {"^1.0.0 || ^3.0.0", "2.0.0", false},
		{"*", "9.9.9", true}, {"", "1.0.0", true}, {"1.2.3", "1.2.3", true}, {"1.2.3", "1.2.4", false},
		{"^1.2.3", "1.5.0-beta.1", false}, {"^1.5.0-beta.1", "1.5.0-beta.2", true}, {">= 1.0.0", "1.0.0", true},
	}
	for _, c := range cases {
		r, ok := ParseRange(c.r)
		if !ok {
			t.Fatalf("range %q did not parse", c.r)
		}
		v, _ := ParseSemver(c.v)
		if got := r.Contains(v); got != c.in {
			t.Errorf("%q contains %s = %v, want %v", c.r, c.v, got, c.in)
		}
	}
	for _, bad := range []string{"github:user/repo", "file:../x", "git+https://x/y.git", "next"} {
		if _, ok := ParseRange(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestRewrite(t *testing.T) {
	cases := []struct {
		c, to, want string
		ok          bool
	}{
		{"^1.2.3", "1.5.0", "^1.5.0", true}, {"~1.2.3", "v1.2.9", "~1.2.9", true}, {"1.2.3", "2.0.0", "2.0.0", true},
		{">=1.0.0", "1.4.0", ">=1.4.0", true}, {">=1 <2", "1.4.0", "", false}, {"1.x || 2.x", "3.0.0", "", false}, {"latest", "1.0.0", "", false},
	}
	for _, c := range cases {
		got, ok := Rewrite(c.c, c.to)
		if got != c.want || ok != c.ok {
			t.Errorf("Rewrite(%q,%q)=%q,%v want %q,%v", c.c, c.to, got, ok, c.want, c.ok)
		}
	}
}

func TestPEP440(t *testing.T) {
	order := []string{"1.0.dev1", "1.0a1", "1.0a2", "1.0b1", "1.0rc1", "1.0", "1.0.post1", "1.0.1", "1.1", "2!0.1"}
	for i := 0; i+1 < len(order); i++ {
		a, ok1 := ParsePEP440(order[i])
		b, ok2 := ParsePEP440(order[i+1])
		if !ok1 || !ok2 {
			t.Fatalf("parse %s %s", order[i], order[i+1])
		}
		if a.Compare(b) != -1 || b.Compare(a) != 1 {
			t.Errorf("%s should sort before %s", order[i], order[i+1])
		}
	}
	a, _ := ParsePEP440("1.0")
	b, _ := ParsePEP440("1.0.0")
	if a.Compare(b) != 0 {
		t.Error("1.0 == 1.0.0")
	}
	c, _ := ParsePEP440("1.0-beta2")
	if c.PreKind != "b" || c.PreNum != 2 || !c.IsPre() {
		t.Errorf("normalization: %+v", c)
	}
	if _, ok := ParsePEP440("not-a-version"); ok {
		t.Error("garbage parsed")
	}
	x, _ := ParsePEP440("2.28.1")
	y, _ := ParsePEP440("2.31.0")
	z, _ := ParsePEP440("3.0.0")
	if KindOfPEP440(x, y) != KindMinor || KindOfPEP440(x, z) != KindMajor || KindOfPEP440(y, x) != KindNone {
		t.Error("kind")
	}
}

func TestTags(t *testing.T) {
	cur := ParseTag("1.22-alpine3.19")
	if !cur.OK || len(cur.Nums) != 2 || cur.Variant != "-alpine3.19" {
		t.Fatalf("%+v", cur)
	}
	if !cur.SameFamily(ParseTag("1.23-alpine3.19")) || cur.SameFamily(ParseTag("1.23-bookworm")) || cur.SameFamily(ParseTag("1.23.1-alpine3.19")) {
		t.Error("families")
	}
	if ParseTag("latest").OK || ParseTag("bookworm").OK {
		t.Error("non-numeric tags are not comparable")
	}
	if !ParseTag("3.0-rc1").Pre || ParseTag("3.0").Pre {
		t.Error("pre detection")
	}
	if ParseTag("v1.2.3").Prefix != "v" {
		t.Error("prefix")
	}
	if KindOfTag(ParseTag("1.22"), ParseTag("2.0")) != KindMajor || KindOfTag(ParseTag("1.22.1"), ParseTag("1.22.3")) != KindPatch || KindOfTag(ParseTag("1.22"), ParseTag("1.21")) != KindNone {
		t.Error("tag kind")
	}
}
