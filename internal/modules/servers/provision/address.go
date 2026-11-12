package provision

import (
	"context"
	"net/netip"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// RenderAddresses is the primary address followed by the additional ones, in
// the order templates and endpoints see them.
func RenderAddresses(srv store.Server, extra []store.Address) []render.Address {
	out := []render.Address{{IP: srv.IP, Hostname: srv.ProxyHostname, Number: 1, Primary: true}}
	for _, a := range extra {
		out = append(out, render.Address{IP: a.IP, Hostname: a.Hostname, Number: a.Position})
	}
	return out
}

// AddressSet is what a new additional address must differ from within one
// server: its primary address, its other additional addresses and the rows
// already accepted.
type AddressSet struct{ IPs, Hosts map[string]bool }

// NewAddressSet starts a set with a server's primary IP and hostnames.
func NewAddressSet(primaryIP string, hosts ...string) *AddressSet {
	a := &AddressSet{IPs: map[string]bool{}, Hosts: map[string]bool{}}
	if _, err := netip.ParseAddr(primaryIP); err == nil {
		a.IPs[primaryIP] = true
	}
	for _, h := range hosts {
		if h != "" {
			a.Hosts[h] = true
		}
	}
	return a
}

// CheckAddress validates one additional address and returns it normalised.
// The refusals are keyed "ip" and "hostname"; the caller maps them to its own
// fields. selfID is the server the address is for (0 for one not stored yet),
// so that one of its own previous IPs reads as a duplicate, not as another
// server's. Whatever passes joins used, so the next row is checked against
// it. q may be a transaction: the check that counts runs in the writing one.
func CheckAddress(ctx context.Context, q sqlx.QueryerContext, selfID int64, used *AddressSet, ip, host string) (nip, nhost string, errs FieldErrors) {
	errs = FieldErrors{}
	nip, nhost = ip, NormalizeHostname(host)
	switch addr, err := netip.ParseAddr(ip); {
	case ip == "":
		fe(errs, "ip", "servers.err.ip_required")
	case err != nil || !addr.Is4() || addr.Is4In6():
		fe(errs, "ip", "servers.err.ip_v4")
	default:
		nip = addr.String()
		checkAddressIP(ctx, q, selfID, used, nip, errs)
	}
	switch {
	case nhost == "":
		fe(errs, "hostname", "servers.err.hostname_required")
	case render.ValidHostname(nhost) != nil:
		fe(errs, "hostname", "servers.err.hostname_invalid")
	default:
		checkAddressHost(ctx, q, used, nhost, errs)
	}
	return nip, nhost, errs
}

func checkAddressIP(ctx context.Context, q sqlx.QueryerContext, selfID int64, used *AddressSet, ip string, errs FieldErrors) {
	if used.IPs[ip] {
		fe(errs, "ip", "servers.err.addr_ip_dup", "ip", ip)
		return
	}
	holder, taken, err := store.HolderOfIP(ctx, q, ip)
	if err == nil && !taken {
		var name string
		if name, taken, err = store.AddressHolder(ctx, q, ip); taken {
			holder = store.IPHolder{Name: name}
		}
	}
	switch {
	case err != nil:
		fe(errs, "ip", "servers.err.addr_check", "error", err.Error())
	case taken && selfID != 0 && holder.ServerID == selfID:
		fe(errs, "ip", "servers.err.addr_ip_dup", "ip", ip)
	case taken:
		fe(errs, "ip", "servers.err.addr_ip_used", "name", holder.Name)
	default:
		used.IPs[ip] = true
	}
}

func checkAddressHost(ctx context.Context, q sqlx.QueryerContext, used *AddressSet, host string, errs FieldErrors) {
	if used.Hosts[host] {
		fe(errs, "hostname", "servers.err.addr_host_dup", "host", host)
		return
	}
	name, taken, err := store.HolderOfHostname(ctx, q, host)
	if err == nil && !taken {
		name, taken, err = store.AddressHostnameHolder(ctx, q, host)
	}
	switch {
	case err != nil:
		fe(errs, "hostname", "servers.err.addr_check", "error", err.Error())
	case taken:
		fe(errs, "hostname", "servers.err.addr_host_used", "name", name)
	default:
		used.Hosts[host] = true
	}
}
