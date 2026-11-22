// Command signin is TEST ONLY: the first sign-in of a new installation, the way a person does it.
// It reads the one-time setup code, creates the first administrator with a password and an
// authenticator code, checks that the session is an administrator's, starts a run of a project and
// waits for it to succeed. Used by the install cells of the upgrade matrix (deploy/test/upgrade).
//
//	signin BASE_URL SETUP_CODE PROJECT
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/localauth"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: signin BASE_URL SETUP_CODE PROJECT")
		os.Exit(2)
	}
	base, code, project := os.Args[1], os.Args[2], os.Args[3]
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	call := func(method, path string, body any) (int, map[string]any) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, base+path, rd) //nolint:gosec // a test helper: the address is the matrix's own port-forward
		req.Header.Set("X-Launchway-CSRF", "1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req) //nolint:gosec // a test helper: the address is the matrix's own port-forward
		if err != nil {
			fmt.Fprintln(os.Stderr, "signin:", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	die := func(f string, a ...any) { fmt.Fprintf(os.Stderr, "signin: "+f+"\n", a...); os.Exit(1) }
	if st, out := call("GET", "/api/v1/runs", nil); st != 503 || out["code"] != "setup_required" {
		die("before sign-in the installation must answer setup_required, got %d %v", st, out)
	}
	pw := "purple walrus drinks coffee"
	st, begin := call("POST", "/api/v1/setup/begin", map[string]string{"code": code, "username": "ada", "display_name": "Ada", "password": pw})
	if st != 200 {
		die("begin: %d %v", st, begin)
	}
	now, _ := localauth.TOTPCode(begin["totp_secret"].(string), time.Now().Unix())
	st, done := call("POST", "/api/v1/setup/complete", map[string]string{"pending": begin["pending"].(string), "code": now})
	if st != 200 || done["signed_in"] != true {
		die("complete: %d %v", st, done)
	}
	if st, me := call("GET", "/api/v1/me", nil); st != 200 || me["admin"] != true {
		die("me: %d %v", st, me)
	}
	fmt.Println("first sign-in: ok, administrator")
	if project == "-" {
		return
	}
	st, run := call("POST", "/api/v1/projects/"+project+"/runs", map[string]any{})
	if st/100 != 2 {
		die("start run: %d %v", st, run)
	}
	id, _ := run["id"].(string)
	for deadline := time.Now().Add(4 * time.Minute); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		st, r := call("GET", "/api/v1/runs/"+id, nil)
		if st == 200 {
			switch r["status"] {
			case "succeeded":
				fmt.Println("run: succeeded")
				return
			case "failed", "canceled":
				die("run %s: %v", id, r["status"])
			}
		}
	}
	die("run %s did not finish", id)
}
