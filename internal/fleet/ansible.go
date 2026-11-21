package fleet

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
)

// The Ansible Maneuver (launchway/ansible-run) takes its hosts and their pinned keys as two text parameters:
// hosts, "name=address[:port]" separated by commas, and known_hosts, the keys in known_hosts format. An
// Airfield's fleet inventory (fleet_hosts) already holds the same facts, each host with its pinned key, so
// the two parameters can be worked out from it instead of typed again.

// AnsibleInputs returns the hosts and known_hosts parameters of the Ansible Maneuver for a fleet, in name
// order. A host without an address or a pinned key is refused, as it is for a fleet delivery: there is no
// "accept new".
func AnsibleInputs(hosts []Host) (hostsParam, knownHosts string, err error) {
	sorted := append([]Host(nil), hosts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var names, keys []string
	for _, h := range sorted {
		if err := h.Validate(); err != nil {
			return "", "", err
		}
		names = append(names, h.Name+"="+h.Address)
		host, port, splitErr := net.SplitHostPort(h.Address)
		if splitErr != nil {
			host, port = h.Address, ""
		}
		pattern := host
		if port != "" && port != "22" {
			pattern = "[" + host + "]:" + port
		}
		key := strings.Join(strings.Fields(h.HostKey), " ")
		if len(strings.Fields(key)) < 2 {
			return "", "", fmt.Errorf("host %s: host_key is the key type and the key, as ssh-keyscan prints them", h.Name)
		}
		keys = append(keys, pattern+" "+key)
	}
	return strings.Join(names, ","), strings.Join(keys, "\n"), nil
}

// AnsibleInputs reads an Airfield's fleet and returns the Ansible Maneuver's hosts and known_hosts.
func (s *Store) AnsibleInputs(ctx context.Context, env string) (hostsParam, knownHosts string, err error) {
	hosts, err := s.Hosts(ctx, env)
	if err != nil {
		return "", "", err
	}
	if len(hosts) == 0 {
		return "", "", fmt.Errorf("the Airfield %s has no fleet inventory", env)
	}
	return AnsibleInputs(hosts)
}
