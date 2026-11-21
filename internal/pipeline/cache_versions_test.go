package pipeline

import (
	"strings"
	"testing"
)

func TestCacheVersionsAreParsedAndChecked(t *testing.T) {
	ok := cacheHead + `
  test:
    image: x
    cache:
      - name: m2
        paths: [.m2]
        key:
          files: [pom.xml]
          versions: [java -version, { run: mvn -v }, { file: .java-version }]
    run: mvn test
`
	p, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	v := p.Step("test").Cache[0].Key.Versions
	if len(v) != 3 || v[0].Run != "java -version" || v[1].Run != "mvn -v" || v[2].File != ".java-version" {
		t.Fatalf("%+v", v)
	}
	for name, bad := range map[string]string{
		"both":    "versions: [{ run: a, file: b }]",
		"neither": "versions: [{}]",
		"empty":   "versions: []",
		"scalar":  "versions: java",
		"escape":  "versions: [{ file: ../x }]",
	} {
		_, err := Parse([]byte(strings.Replace(ok, "versions: [java -version, { run: mvn -v }, { file: .java-version }]", bad, 1)))
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
