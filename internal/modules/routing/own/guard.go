package own

import "github.com/tikhonp/proxier/internal/modules/routing/domain"

// Covering reports the first server whose hostname the name equals or, as a
// suffix, covers: such a name would send Proxier's own traffic to the server
// into the router's tunnel. An exact name covers only an equal hostname.
func Covering(name string, exact bool, servers []Server) (server, hostname string, ok bool) {
	for _, s := range servers {
		for _, h := range s.Hostnames {
			if h == name || !exact && domain.Covers(name, h) {
				return s.Name, h, true
			}
		}
	}
	return "", "", false
}
