package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// TrustedProxyPrefixes parses server.trusted_proxies: CIDR ranges, or single
// addresses.
func (s ServerConfig) TrustedProxyPrefixes() ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, c := range s.TrustedProxies {
		c = strings.TrimSpace(c)
		p, err := netip.ParsePrefix(c)
		if err != nil {
			a, aerr := netip.ParseAddr(c)
			if aerr != nil {
				return nil, fmt.Errorf("server.trusted_proxies: %q is not an address range such as 10.0.0.0/8 or 192.0.2.7", c)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p.Masked())
	}
	if h := s.ClientAddressHeader; h != "" && (strings.ContainsAny(h, " :\t,") || len(h) > 60) {
		return nil, fmt.Errorf("server.client_address_header: %q is not a header name", h)
	}
	return out, nil
}
