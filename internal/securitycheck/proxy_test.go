package securitycheck

import (
	"strings"
	"testing"
)

func TestDoctorReportsAddressesBelievedFromAnyPeer(t *testing.T) {
	find := func(f Facts) *Item {
		for _, it := range Check(f) {
			if it.ID == "proxy-addresses" {
				c := it
				return &c
			}
		}
		return nil
	}
	if it := find(Facts{ProxyNamed: No}); it == nil || it.State != "attention" || !strings.Contains(it.Detail, "believed from any peer") {
		t.Errorf("trust_proxy alone: %+v", it)
	}
	if it := find(Facts{ProxyNamed: Yes}); it == nil || it.State == "attention" {
		t.Errorf("named proxies: %+v", it)
	}
	if it := find(Facts{}); it != nil {
		t.Errorf("no proxy in use is not listed: %+v", it)
	}
}
