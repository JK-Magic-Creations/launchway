package testreport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.*"))
	for _, name := range files {
		if b, err := os.ReadFile(name); err == nil {
			f.Add(b)
		}
	}
	for _, s := range []string{
		"",
		"<testsuite/>",
		"<testsuite><testcase name=\"a\x00\"><failure>\x1b[31mx</failure><system-out>&#27;[0m</system-out></testcase>",
		"<!DOCTYPE x [<!ENTITY a \"&b;\">]><testsuites><testsuite><testsuite><testcase/>",
		`{"Action":"run","Package":"p","Test":"T"}` + "\n" + `{"Action":"output","Package":"p","Test":"T","Output":"panic: x\n"}`,
		"go: x\n{\"Action\":\"fail\",\"Package\":\"p\"}\n",
		"<?xml version='1.0' encoding='latin1'?><testsuite name='\xff'><testcase name='\x80'/></testsuite>",
	} {
		f.Add([]byte(s))
	}
	lim := Limits{MaxCases: 20}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Parse(context.Background(), data, lim)
		if err != nil {
			if errors.Is(err, ErrNotReport) && !strings.HasPrefix(err.Error(), "not a test report: ") {
				t.Errorf("error = %q", err)
			}
			checkString(t, "error", err.Error(), 1000)
			return
		}
		checkReport(t, r, lim)
	})
}
