package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runs show prints, under each step that ran, what it reserved and used, and
// says so plainly when it could not be measured.
func TestCLIShowsWhatAStepUsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"run_1","project":"app","number":4,"status":"succeeded","event":"manual","branch":"main","sha":"0123456789abcdef","actor":"ann","created_at":1,
			"step_list":[{"id":"test","status":"succeeded","attempt":1,"usage":{"measured":true,"memory_peak_bytes":1181116006,"cpu_peak_milli":1900,"reserved_memory_bytes":4294967296}},
			{"id":"lint","status":"succeeded","attempt":1,"usage":{"measured":false,"note":"no cgroup"}},{"id":"old","status":"succeeded","attempt":1}]}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out).show([]string{"run_1"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"reserved 4.0Gi memory, used 1.1Gi memory at most and 1.9 CPU at most", "use not measured: no cgroup"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}
