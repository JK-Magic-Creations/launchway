package testdb

import (
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Proxy is a TCP proxy in front of a PostgreSQL server, for failure
// injection: it can drop every connection and refuse new ones (the database
// is gone), and delay what passes (a slow network). Tests point the engine at
// the proxy and switch it.
type Proxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	down   bool
	delay  time.Duration
	conns  map[net.Conn]struct{}
}

// ThroughProxy returns addr rewritten to go through a new Proxy. It skips the
// test when the tests do not run on PostgreSQL.
func ThroughProxy(t testing.TB, addr string) (string, *Proxy) {
	t.Helper()
	if !Postgres() {
		t.Skip("needs PostgreSQL: set " + EnvVar)
	}
	u, err := url.Parse(addr)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{ln: ln, target: u.Host, conns: map[net.Conn]struct{}{}}
	t.Cleanup(p.Close)
	go p.serve()
	u.Host = ln.Addr().String()
	return u.String(), p
}

// SetDown drops every open connection and, until it is set false again,
// closes new ones at once.
func (p *Proxy) SetDown(down bool) {
	p.mu.Lock()
	p.down = down
	var drop []net.Conn
	if down {
		for c := range p.conns {
			drop = append(drop, c)
		}
	}
	p.mu.Unlock()
	for _, c := range drop {
		c.Close()
	}
}

// SetDelay makes each chunk of data wait d before it is passed on.
func (p *Proxy) SetDelay(d time.Duration) { p.mu.Lock(); p.delay = d; p.mu.Unlock() }

// Close stops the proxy.
func (p *Proxy) Close() { p.ln.Close(); p.SetDown(true) }

func (p *Proxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		down := p.down
		p.mu.Unlock()
		if down {
			c.Close()
			continue
		}
		go p.pipe(c)
	}
}

func (p *Proxy) pipe(c net.Conn) {
	up, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", p.target)
	if err != nil {
		c.Close()
		return
	}
	p.mu.Lock()
	p.conns[c], p.conns[up] = struct{}{}, struct{}{}
	p.mu.Unlock()
	done := func() {
		c.Close()
		up.Close()
		p.mu.Lock()
		delete(p.conns, c)
		delete(p.conns, up)
		p.mu.Unlock()
	}
	copyWithDelay := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				p.mu.Lock()
				d := p.delay
				p.mu.Unlock()
				if d > 0 {
					time.Sleep(d)
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				if err != io.EOF {
					_ = err
				}
				break
			}
		}
		done()
	}
	go copyWithDelay(up, c)
	go copyWithDelay(c, up)
}
