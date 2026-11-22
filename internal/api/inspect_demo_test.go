package api

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/deploy/inspect/inspectfake"
)

// A demo server for looking at the Launch page in a browser, backed by the fake cluster. It runs
// only when LAUNCHWAY_INSPECT_DEMO names an address (host:port); LAUNCHWAY_INSPECT_DEMO_UI is the
// built web UI, and LAUNCHWAY_INSPECT_DEMO_BROKEN=1 makes the canary crash-loop. It prints the
// address of the page, and stops when the file named by LAUNCHWAY_INSPECT_DEMO_STOP exists (or
// after 30 minutes).
func TestInspectDemoServer(t *testing.T) {
	addr := os.Getenv("LAUNCHWAY_INSPECT_DEMO")
	if addr == "" {
		t.Skip("set LAUNCHWAY_INSPECT_DEMO=host:port to run the demo server")
	}
	inspectfake.T0 = time.Now()
	ie := setupInspectWith(t, argocd.Inspect{Read: true, Logs: true, Control: true, ScaleMax: 10}, os.Getenv("LAUNCHWAY_INSPECT_DEMO_BROKEN") == "1")
	s := &Server{Engine: ie.eng2, RunnerToken: "secret", Actor: "ada", Log: quiet(), UI: os.DirFS(os.Getenv("LAUNCHWAY_INSPECT_DEMO_UI"))}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	println("LAUNCH_URL=http://" + addr + "/runs/" + ie.runID + "/launch/" + ie.dep)
	stop := os.Getenv("LAUNCHWAY_INSPECT_DEMO_STOP")
	for i := 0; i < 1800; i++ {
		if _, err := os.Stat(stop); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
}
