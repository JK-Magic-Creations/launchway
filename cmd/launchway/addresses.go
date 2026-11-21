package main

import (
	"fmt"
	"net"
	neturl "net/url"
)

// describeDatabase names the database without revealing a password.
func describeDatabase(addr string) string {
	u, err := neturl.Parse(addr)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "SQLite database " + addr
	}
	return "PostgreSQL database " + u.Redacted()
}

// listening says where the server listens and, when that is every address,
// how to reach it from this machine.
func listening(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if ip := net.ParseIP(host); err == nil && ip != nil && ip.IsUnspecified() {
		return fmt.Sprintf("%s, every address; from this machine http://%s", addr, loopback(addr.String()))
	}
	return "http://" + addr.String()
}

// loopback turns 0.0.0.0:8080 or [::]:8080 into 127.0.0.1:8080.
func loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
