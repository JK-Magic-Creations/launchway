package fleet

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// A tiny SSH server that answers every command with its text.
func sshServer(t *testing.T, hostSigner ssh.Signer, user ssh.PublicKey) string {
	t.Helper()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) == string(user.Marshal()) {
			return nil, nil
		}
		return nil, context.Canceled
	}}
	cfg.AddHostKey(hostSigner)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, rq, _ := nc.Accept()
					go func() {
						for r := range rq {
							if r.Type == "exec" {
								cmd := string(r.Payload[4:])
								r.Reply(true, nil)
								io.Copy(io.Discard, ch)
								ch.Write([]byte("ran " + cmd))
								ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
								ch.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return l.Addr().String()
}

func TestSSHPinsHostKeyAndAuditsEveryCommand(t *testing.T) {
	hostSigner, hostPub := hostKey(t)
	userSigner, _ := hostKey(t)
	addr := sshServer(t, hostSigner, userSigner.PublicKey())
	r := &SSH{Signer: userSigner}
	h := Host{Name: "web-1", Address: addr, HostKey: hostPub, Service: "x"}

	var entries []AuditEntry
	var mu sync.Mutex
	a := &Audited{Remote: r, RunID: "run-1", Sink: func(e AuditEntry) { mu.Lock(); entries = append(entries, e); mu.Unlock() }}
	res, err := a.Run(context.Background(), h, "uptime", strings.NewReader("secret-free payload"))
	if err != nil || res.Stdout != "ran uptime" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(entries) != 2 || entries[0].Done || !entries[1].Done || entries[1].Command != "uptime" || entries[1].StdinLen == 0 || entries[1].StdinSHA == "" || entries[1].Run != "run-1" {
		t.Fatalf("audit: %+v", entries)
	}
	// Another key: refused, and the refusal is audited too.
	_, otherPub := hostKey(t)
	h.HostKey = otherPub
	_, err = a.Run(context.Background(), h, "uptime", nil)
	if err == nil || !strings.Contains(err.Error(), ErrHostKey.Error()) {
		t.Fatalf("a wrong host key was accepted: %v", err)
	}
	if last := entries[len(entries)-1]; last.Error == "" {
		t.Fatalf("the refusal was not audited: %+v", last)
	}
	// A host with no pinned key never validates.
	h.HostKey = ""
	if h.Validate() == nil {
		t.Fatal("a host with no pinned key is valid")
	}
}
