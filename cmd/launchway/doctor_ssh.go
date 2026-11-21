package main

import (
	"flag"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/config"
)

// sshProbe connects to an SSH address and reads the host key it presents for
// one algorithm, without signing in.
func sshProbe(addr, algo string, timeout time.Duration) (ssh.PublicKey, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{User: "git", HostKeyAlgorithms: []string{algo}, Timeout: timeout,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error { got = k; return nil }}
	c, _, _, err := ssh.NewClientConn(conn, addr, cfg)
	if err == nil {
		_ = c.Close()
	}
	if got != nil {
		return got, nil // the handshake got as far as the host key: whatever came after is the sign-in
	}
	return nil, err
}

// doctorSSH reports whether Git over SSH is reachable and which host keys it
// presents. The address is --address, or the installation's forge.ssh settings
// in the configuration file.
func doctorSSH(args []string) error {
	fs := flag.NewFlagSet("doctor ssh", flag.ExitOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	addr := fs.String("address", "", "HOST:PORT to check (default: forge.ssh.public_host and public_port, or listen, from the configuration file)")
	_ = fs.Parse(args)
	target := *addr
	if target == "" {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		s := cfg.Forge.SSH
		if !s.On() {
			fmt.Println("Git over SSH: off (forge.ssh.listen is not set in the configuration file). Pass --address HOST:PORT to check a running server.")
			return nil
		}
		host := s.PublicHost
		if host == "" {
			host = "127.0.0.1"
		}
		target = net.JoinHostPort(host, strconv.Itoa(s.Port()))
	}
	fmt.Printf("Git over SSH at %s\n", target)
	bad := 0
	for _, algo := range []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512} {
		k, err := sshProbe(target, algo, 10*time.Second)
		if err != nil {
			fmt.Printf("  %-22s not reachable: %v\n", algo, err)
			bad++
			continue
		}
		fmt.Printf("  %-22s %s\n", k.Type(), ssh.FingerprintSHA256(k))
	}
	switch bad {
	case 0:
		fmt.Println("Reachable. Compare these fingerprints with the host key page or `launchway keys host`.")
		return nil
	case 3:
		return fmt.Errorf("nothing answered as an SSH server at %s: check the Service or load balancer, a firewall, and that forge.ssh.listen is set and servers were restarted", target)
	}
	return fmt.Errorf("%d of the 3 host keys could not be read from %s", bad, target)
}
