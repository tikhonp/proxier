package fetch

import "net/netip"

// Network is the network a client address belongs to for shared-link
// alerts: its IPv4 /24 or IPv6 /48 ("198.51.100.0/24", "2001:db8:4f2::/48").
// An IPv4 address mapped into IPv6 counts as IPv4.
func Network(ip netip.Addr) string {
	if !ip.IsValid() {
		return ""
	}
	ip = ip.Unmap()
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	p, err := ip.Prefix(bits)
	if err != nil {
		return ""
	}
	return p.Masked().String()
}
