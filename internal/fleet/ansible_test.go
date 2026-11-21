package fleet

import (
	"strings"
	"testing"
)

func TestAnsibleInputsComeFromTheFleet(t *testing.T) {
	hosts := []Host{
		{Name: "web2", Address: "10.0.0.12:2222", User: "deploy", HostKey: "ssh-ed25519 AAAAB", Service: "10.0.0.12:8080"},
		{Name: "web1", Address: "10.0.0.11", HostKey: "ssh-ed25519   AAAAA", Service: "10.0.0.11:8080"},
	}
	h, k, err := AnsibleInputs(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if h != "web1=10.0.0.11,web2=10.0.0.12:2222" {
		t.Fatalf("hosts %q", h)
	}
	if k != "10.0.0.11 ssh-ed25519 AAAAA\n[10.0.0.12]:2222 ssh-ed25519 AAAAB" {
		t.Fatalf("known_hosts %q", k)
	}
	// A host with no pinned key is refused, as it is for a delivery.
	hosts[0].HostKey = ""
	if _, _, err := AnsibleInputs(hosts); err == nil || !strings.Contains(err.Error(), "host_key") {
		t.Fatalf("a host without a pinned key: %v", err)
	}
	hosts[0].HostKey = "AAAAB"
	if _, _, err := AnsibleInputs(hosts); err == nil || !strings.Contains(err.Error(), "key type") {
		t.Fatalf("a key without its type: %v", err)
	}
}
