package securitycheck

import (
	"strings"
	"testing"
)

func find(items []Item, id string) *Item {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}

func TestNoSignInIsAnAttentionItemWithAWayForward(t *testing.T) {
	it := find(Check(Facts{SignIn: No, Loopback: Yes}), "sign-in")
	if it == nil || it.State != "attention" || !strings.Contains(it.Detail, "built-in accounts") {
		t.Fatalf("%+v", it)
	}
}

func TestPlainHTTPOnAPrivateNetworkNeedsAttentionButLoopbackDoesNot(t *testing.T) {
	if it := find(Check(Facts{SignIn: Yes, Loopback: No, TLS: No}), "tls"); it == nil || it.State != "attention" {
		t.Fatalf("plain http beyond this machine: %+v", it)
	}
	if find(Check(Facts{SignIn: Yes, Loopback: Yes, TLS: Yes}), "tls") != nil {
		t.Fatal("a loopback address has nothing to encrypt in transit")
	}
}

func TestUnknownFactsAreSaidNotGuessed(t *testing.T) {
	items := Check(Facts{})
	if it := find(items, "encryption"); it == nil || it.State != "unknown" {
		t.Fatalf("%+v", it)
	}
	if find(items, "network-policies") != nil {
		t.Fatal("cluster-only items must not appear for a machine install")
	}
	if it := find(Check(Facts{Kubernetes: true, NetworkPolicies: No}), "network-policies"); it == nil || it.State != "attention" || !strings.Contains(it.Detail, "does not enforce") {
		t.Fatalf("%+v", it)
	}
}

func TestRootKeyCopyIsAskedForOnlyWithEncryption(t *testing.T) {
	if it := find(Check(Facts{Encrypted: No, RootKeyCopied: Unknown}), "root-key-copy"); it != nil {
		t.Fatalf("no key, no copy to ask about: %+v", it)
	}
	if it := find(Check(Facts{Encrypted: Yes, RootKeyCopied: No}), "root-key-copy"); it == nil || it.State != "attention" {
		t.Fatalf("%+v", it)
	}
	if Attention(Check(Facts{SignIn: Yes, Encrypted: Yes, RootKeyCopied: Yes, Loopback: Yes, PublicExposure: Yes})) != 0 {
		t.Fatal("a good installation has nothing to attend to")
	}
}
