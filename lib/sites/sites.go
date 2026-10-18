// Package sites names the site a line came from by the network its sender is in: a gateway at home and one at the cabin send to the same relay,
// and the site tells their rows apart.
package sites

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Field is the field a row's site is stored in.
const Field = "site"

// Networks are the sites and the networks each is at. The zero value names no site.
type Networks struct {
	// most specific network first, so the first that holds an address is the one it is in
	list []network
}

type network struct {
	prefix netip.Prefix
	site   string
}

// Parse reads the sites from a setting written site=network,site=network: home=10.0.0.0/16,cabin=10.2.0.0/16. A site at more than one network is
// given once for each, and a network can be a single address. Nothing at all is no sites.
func Parse(s string) (Networks, error) {
	var n Networks
	for pair := range strings.SplitSeq(s, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}

		site, at, found := strings.Cut(pair, "=")
		site, at = strings.TrimSpace(site), strings.TrimSpace(at)
		if !found || site == "" || at == "" {
			return Networks{}, fmt.Errorf("%q is not site=network, e.g. home=10.0.0.0/16", pair)
		}

		prefix, err := parsePrefix(at)
		if err != nil {
			return Networks{}, fmt.Errorf("%q: %w", pair, err)
		}

		if i := slices.IndexFunc(n.list, func(nw network) bool { return nw.prefix == prefix }); i >= 0 {
			if n.list[i].site != site {
				return Networks{}, fmt.Errorf("%s is given to both %s and %s", prefix, n.list[i].site, site)
			}

			continue
		}

		n.list = append(n.list, network{prefix: prefix, site: site})
	}

	slices.SortStableFunc(n.list, func(a, b network) int { return cmp.Compare(b.prefix.Bits(), a.prefix.Bits()) })

	return n, nil
}

// parsePrefix reads a network, or a single address as the network of just that address. 10.0.0.1/16 is read as 10.0.0.0/16.
func parsePrefix(s string) (netip.Prefix, error) {
	if !strings.Contains(s, "/") {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a network or an address", s)
		}

		addr = addr.Unmap()

		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}

	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a network or an address", s)
	}

	return prefix.Masked(), nil
}

// Site is the site of the sender at that address, written as remote_ip is: the site at the most specific network that holds it, "" for an
// address in none.
func (n Networks) Site(addr string) string {
	ip, err := netip.ParseAddr(strings.Trim(addr, "[]"))
	if err != nil {
		return ""
	}
	ip = ip.Unmap().WithZone("")

	for _, nw := range n.list {
		if nw.prefix.Contains(ip) {
			return nw.site
		}
	}

	return ""
}
