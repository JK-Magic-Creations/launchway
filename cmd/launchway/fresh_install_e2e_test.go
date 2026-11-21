package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/localauth"
)

// The experience of a stranger, against the real binary: a fresh data directory, the setup code from the
// start-up message, the first administrator with an authenticator, signing out and in again with the second
// factor, and the console command that issues a new setup code before setup and refuses after it.
func TestFreshInstallWithBuiltInAccounts(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "launchway")
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	cfgPath := filepath.Join(dir, "launchway.yaml")
	data := filepath.Join(dir, "data")
	os.WriteFile(cfgPath, []byte(fmt.Sprintf("listen: %s\ndata: %s\nauth:\n  mode: local\n", addr, data)), 0o600)

	cmd := exec.Command(bin, "server", "--config", cfgPath)
	var log bytes.Buffer
	pipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); _, _ = cmd.Process.Wait() })
	codeRe := regexp.MustCompile(`[A-Z0-9]{5}(-[A-Z0-9]{5}){4}`)
	codeCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			log.WriteString(sc.Text() + "\n")
			if m := codeRe.FindString(sc.Text()); m != "" && len(codeCh) == 0 {
				codeCh <- m
			}
		}
	}()
	var code string
	select {
	case code = <-codeCh:
	case <-time.After(60 * time.Second):
		t.Fatalf("no setup code in the start-up message:\n%s", log.String())
	}
	base := "http://" + addr
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	call := func(method, path string, body any) (int, map[string]any) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("X-Launchway-CSRF", "1")
		req.Header.Set("Content-Type", "application/json")
		var resp *http.Response
		var err error
		for i := 0; i < 50; i++ { // the server may still be coming up
			if resp, err = hc.Do(req); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
			req, _ = http.NewRequest(method, base+path, rd)
			req.Header.Set("X-Launchway-CSRF", "1")
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if st, out := call("GET", "/api/v1/runs", nil); st != 503 || out["code"] != "setup_required" {
		t.Fatalf("before setup: %d %v", st, out)
	}
	pw := "purple walrus drinks coffee"
	st, begin := call("POST", "/api/v1/setup/begin", map[string]string{"code": code, "username": "ada", "display_name": "Ada", "password": pw})
	if st != 200 {
		t.Fatalf("begin %d %v", st, begin)
	}
	secret := begin["totp_secret"].(string)
	now, _ := localauth.TOTPCode(secret, time.Now().Unix())
	st, done := call("POST", "/api/v1/setup/complete", map[string]string{"pending": begin["pending"].(string), "code": now})
	if st != 200 || done["signed_in"] != true {
		t.Fatalf("complete %d %v", st, done)
	}
	if st, me := call("GET", "/api/v1/me", nil); st != 200 || me["admin"] != true {
		t.Fatalf("me %d %v", st, me)
	}
	if st, _ := call("POST", "/auth/logout", nil); st != 204 && st != 200 {
		t.Fatalf("logout %d", st)
	}
	if st, _ := call("GET", "/api/v1/me", nil); st != 401 {
		t.Fatalf("after sign out: %d", st)
	}
	// Sign in again: password, then the next authenticator code (the step used at setup cannot be reused).
	st, one := call("POST", "/api/v1/auth/local/signin", map[string]string{"username": "ada", "password": pw})
	if st != 200 || one["second_factor"] != "totp" {
		t.Fatalf("sign-in %d %v", st, one)
	}
	// The server judges by its own clock: wait for the next step so the code is the current one.
	time.Sleep(time.Until(time.Now().Truncate(30 * time.Second).Add(30 * time.Second)))
	later, _ := localauth.TOTPCode(secret, time.Now().Unix())
	if st, out := call("POST", "/api/v1/auth/local/second-factor", map[string]string{"ticket": one["ticket"].(string), "code": later}); st != 200 {
		t.Fatalf("second factor %d %v", st, out)
	}
	if st, _ := call("GET", "/api/v1/me", nil); st != 200 {
		t.Fatalf("signed in again: %d", st)
	}
	// The console: a setup code after setup is refused, and the accounts are listed.
	out, err := exec.Command(bin, "users", "setup-code", "--config", cfgPath).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "already completed") {
		t.Fatalf("setup-code after setup: %v %s", err, out)
	}
	out, err = exec.Command(bin, "users", "list", "--config", cfgPath).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ada") || strings.Contains(string(out), "argon2") {
		t.Fatalf("users list: %v %s", err, out)
	}
	out, err = exec.Command(bin, "users", "reset", "--config", cfgPath, "ada").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "/accept?token=") {
		t.Fatalf("users reset: %v %s", err, out)
	}
}
