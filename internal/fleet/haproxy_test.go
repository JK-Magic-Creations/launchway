package fleet

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

const statSample = `# pxname,svname,qcur,qmax,scur,smax,slim,stot,bin,bout,dreq,dresp,ereq,econ,eresp,wretr,wredis,status,weight,act,bck,chkfail,chkdown,lastchg,downtime,qlimit,pid,iid,sid,throttle,lbtot,tracked,type,rate,rate_lim,rate_max,check_status
app,FRONTEND,,,3,10,100,50,0,0,0,0,0,,,,,OPEN,,,,,,,,,1,1,0,,,,0,5,0,9,
app,web-1,0,0,2,4,,25,0,0,,0,,0,0,0,0,UP 1/2,10,1,0,0,0,10,0,,1,2,1,,25,,2,2,,4,L7OK
app,web-2,0,0,0,4,,25,0,0,,0,,0,0,0,0,DRAIN,0,1,0,0,0,10,0,,1,2,2,,25,,2,2,,4,L7OK
app,BACKEND,0,0,2,10,10,50,0,0,0,0,,0,0,0,0,UP,10,1,0,,0,10,0,,1,2,0,,50,,1,5,,9,
`

func TestParseStat(t *testing.T) {
	m, err := parseStat(statSample)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["app/web-1"].State != "up" || m["app/web-1"].Sessions != 2 || m["app/web-1"].Weight != 10 ||
		m["app/web-2"].State != "drain" || m["app/web-2"].Weight != 0 {
		t.Fatalf("%+v", m)
	}
}

// A fake admin socket: records commands, answers like HAProxy does.
func TestHAProxyCommands(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var mu sync.Mutex
	var got []string
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				line = strings.TrimSpace(line)
				mu.Lock()
				got = append(got, line)
				mu.Unlock()
				switch {
				case line == "show stat":
					c.Write([]byte(statSample))
				case strings.Contains(line, "app/nope"):
					c.Write([]byte("No such server.\n"))
				}
			}()
		}
	}()
	h := &HAProxy{Network: "tcp", Address: l.Addr().String()}
	ctx := context.Background()
	if err := h.Drain(ctx, "app/web-1"); err != nil {
		t.Fatal(err)
	}
	if err := h.SetWeight(ctx, "app/web-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := h.Admit(ctx, "app/web-1"); err != nil {
		t.Fatal(err)
	}
	if err := h.Admit(ctx, "app/nope"); err == nil || !strings.Contains(err.Error(), "No such server") {
		t.Fatalf("a refusal was not reported: %v", err)
	}
	// Injection: a member name with a newline or space is never sent.
	for _, bad := range []string{"app/web-1\nshutdown sessions", "app/web 1", "web-1", "a/b/c"} {
		if err := h.Drain(ctx, bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if err := h.SetWeight(ctx, "app/web-1", 999); err == nil {
		t.Error("a weight of 999 was accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"set server app/web-1 state drain", "set server app/web-1 weight 7", "set server app/web-1 state ready", "set server app/nope state ready"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sent %q", got)
	}
}
