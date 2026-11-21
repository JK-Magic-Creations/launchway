package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The doctor reads the host key an SSH server presents, and says plainly
// when nothing answers.
func TestDoctorSSHReadsTheHostKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				if sc, _, reqs, err := ssh.NewServerConn(c, cfg); err == nil {
					go ssh.DiscardRequests(reqs)
					time.Sleep(50 * time.Millisecond)
					sc.Close()
				}
			}()
		}
	}()
	k, err := sshProbe(ln.Addr().String(), ssh.KeyAlgoED25519, 5*time.Second)
	if err != nil || ssh.FingerprintSHA256(k) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("probe: %v %v", k, err)
	}
	// Another algorithm than the server has: no key, and an error that says so.
	if _, err := sshProbe(ln.Addr().String(), ssh.KeyAlgoECDSA256, 5*time.Second); err == nil {
		t.Error("a host key algorithm the server does not have was read")
	}
	// Nothing listening.
	dead := ln.Addr().String()
	ln.Close()
	if _, err := sshProbe(dead, ssh.KeyAlgoED25519, time.Second); err == nil {
		t.Error("a closed port was reached")
	}
	if err := doctorSSH([]string{"--address", dead}); err == nil {
		t.Error("doctor ssh reported success for a closed port")
	}
}
