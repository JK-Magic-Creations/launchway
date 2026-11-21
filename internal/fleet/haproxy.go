package fleet

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// HAProxy drives HAProxy through its runtime API: drain and ready states,
// weights and per-server session counts, all without a reload and without
// dropping a connection. HAProxy is run separately (a container or a
// service); Launchway only talks to its admin socket and never links it.
//
// The socket gives full control of the balancer, so it must never be open
// to a network: use a unix socket, or a TCP listener on loopback or a
// private network that only the Station reaches.
type HAProxy struct {
	// Network and Address of the admin socket: ("unix", "/run/haproxy/admin.sock")
	// or ("tcp", "127.0.0.1:9999").
	Network, Address string
	Timeout          time.Duration
}

var memberRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}/[A-Za-z0-9_.-]{1,64}$`)

func (h *HAProxy) cmd(ctx context.Context, line string) (string, error) {
	d := net.Dialer{Timeout: orDur(h.Timeout, 5*time.Second)}
	c, err := d.DialContext(ctx, h.Network, h.Address)
	if err != nil {
		return "", fmt.Errorf("the load balancer's admin socket: %w", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(orDur(h.Timeout, 5*time.Second) * 2))
	if _, err := io.WriteString(c, line+"\n"); err != nil {
		return "", err
	}
	b, err := io.ReadAll(bufio.NewReader(c))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (h *HAProxy) set(ctx context.Context, member, what string) error {
	if !memberRe.MatchString(member) {
		return fmt.Errorf("%q is not a load balancer member (backend/server)", member)
	}
	out, err := h.cmd(ctx, "set server "+member+" "+what)
	if err != nil {
		return err
	}
	// A successful set prints nothing; anything else is the balancer
	// explaining a refusal.
	if out != "" {
		return fmt.Errorf("the load balancer refused set server %s %s: %s", member, what, out)
	}
	return nil
}

// Drain implements Traffic.
func (h *HAProxy) Drain(ctx context.Context, m string) error { return h.set(ctx, m, "state drain") }

// Admit implements Traffic.
func (h *HAProxy) Admit(ctx context.Context, m string) error { return h.set(ctx, m, "state ready") }

// SetWeight implements Traffic.
func (h *HAProxy) SetWeight(ctx context.Context, m string, w int) error {
	if w < 0 || w > 256 {
		return fmt.Errorf("weight %d is outside 0 to 256", w)
	}
	return h.set(ctx, m, "weight "+strconv.Itoa(w))
}

// Observe implements Traffic.
func (h *HAProxy) Observe(ctx context.Context) (map[string]Member, error) {
	out, err := h.cmd(ctx, "show stat")
	if err != nil {
		return nil, err
	}
	return parseStat(out)
}

func parseStat(out string) (map[string]Member, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(out, "# ")))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil || len(recs) == 0 {
		return nil, fmt.Errorf("the load balancer's statistics could not be read: %v", err)
	}
	col := map[string]int{}
	for i, n := range recs[0] {
		col[n] = i
	}
	for _, need := range []string{"pxname", "svname", "scur", "status", "weight"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("the load balancer's statistics lack the column %q", need)
		}
	}
	res := map[string]Member{}
	for _, rec := range recs[1:] {
		if len(rec) <= col["weight"] {
			continue
		}
		sv := rec[col["svname"]]
		if sv == "FRONTEND" || sv == "BACKEND" {
			continue
		}
		m := Member{Member: rec[col["pxname"]] + "/" + sv, State: firstWord(rec[col["status"]]), Health: rec[col["check_status"]]}
		m.Sessions, _ = strconv.Atoi(rec[col["scur"]])
		m.Weight, _ = strconv.Atoi(rec[col["weight"]])
		res[m.Member] = m
	}
	return res, nil
}

// firstWord turns "UP 1/2" (up, still counting its successes) into "up".
func firstWord(s string) string {
	if f := strings.Fields(strings.ToLower(s)); len(f) > 0 {
		return f[0]
	}
	return ""
}
