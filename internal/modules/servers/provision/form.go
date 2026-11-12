// Package provision builds a server from a template: the new-server form with
// its checks and live summary, Create, and the servers.provision job with its
// retry and Activate anyway (docs/processes/servers/server-provisioning.md).
package provision

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/geoip"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// FormAddress is one additional address row of the new-server form.
type FormAddress struct {
	IP, Hostname string
	// row is the row's place in the form, for the error of the check that
	// runs again in the writing transaction.
	row int
}

// Form is what the new-server form posts.
type Form struct {
	IP, RootPassword string
	// SSHIP overrides the connection address only (e.g. a Tailscale IP).
	// IP and Addresses remain the service addresses exposed to clients.
	SSHIP   string
	SSHPort int // 0 = 22
	// AccessMode is how Proxier logs in: store.AccessPassword (root's password
	// creates the proxier user) or store.AccessKey (Proxier's key is already
	// in SSHUser's authorized_keys). "" takes the Settings → Servers default.
	AccessMode string
	// SSHUser is the account whose authorized_keys holds Proxier's key in key
	// access; "" takes root.
	SSHUser string
	// Setup is store.SetupFull (hardening included) or store.SetupMinimal
	// (prerequisites and Docker only); "" takes the Settings → Servers default.
	Setup string
	// Country is the ISO code the picker gave, or the text typed in it when
	// that names no country (refused as such).
	Country    string
	TemplateID int64
	Version    int // 0 = the template's default version
	Params     map[string]string
	Notes      string
	// Hostname is the server's management and proxy hostname; "" takes the
	// name the hostname pattern gives.
	Hostname string
	// CheckDNS asks the live summary to check a record the member manages.
	// Create checks it too. SkipDNS disables both checks.
	CheckDNS bool
	// SkipDNS skips the form check and provisioning wait for a record the
	// member manages. Certificate issuance and connection tests still run.
	SkipDNS bool
	// Addresses are the server's additional addresses: more IPs on the same
	// machine, each with its own hostname. Proxier does not manage their DNS.
	Addresses []FormAddress
}

// Msg is a refusal: an i18n key and its arguments.
type Msg struct {
	Key  string
	Args i18n.Args
}

// FieldErrors maps a form field ("ip", "param.letsencrypt_email") to why it
// was refused. An operation that returns FieldErrors changed nothing.
type FieldErrors map[string]Msg

func (fe FieldErrors) Error() string {
	parts := make([]string, 0, len(fe))
	for k, v := range fe {
		parts = append(parts, k+": "+v.Key)
	}
	return "invalid: " + strings.Join(parts, "; ")
}

// Summary is what the live summary shows while the admin types.
type Summary struct {
	Name, ManagementHostname, ProxyHostname string
	SSHIP                                   string
	// DNSRecord is the record that will be written: "A <name> → <ip>, DNS only, TTL 60",
	// or with ManualDNS the one the member creates: "A <name> → <ip>".
	DNSRecord string
	// ManualDNS: the hostname is in none of the Cloudflare zones, so the
	// member creates its record by hand at their DNS provider.
	ManualDNS bool
	// DNSFound is what the check of a record made by hand found when it is
	// in place ("ns1.example.net answered 203.0.113.10"); a record that is
	// not is in Errors["dns"].
	DNSFound string
	// Addresses are the additional addresses as they will be stored:
	// "203.0.113.25 → alt.example.com".
	Addresses []string
	Endpoints []string // display names
	// Suggested is the IP's country, "" when unknown or not asked for.
	Suggested string
	Errors    FieldErrors
}

// geoTTL is how long a country lookup is kept per IP.
const geoTTL = 10 * time.Minute

type geoEntry struct {
	country string
	at      time.Time
}

type geoCache struct {
	mu sync.Mutex
	m  map[string]geoEntry
}

// checked is a validated form with what was looked up on the way.
type checked struct {
	form     Form
	prefix   string // the first half of the name; "" until the country is valid
	tpl      store.Template
	version  int
	files    map[string][]byte
	man      *manifest.Manifest
	number   int
	hostname string
	// manualDNS: a typed hostname in none of the Cloudflare zones; found is
	// what its check found, when it ran and passed.
	manualDNS bool
	found     string
	public    map[string]string // non-secret parameters, as stored
	secret    map[string]string
}

func fe(errs FieldErrors, field, key string, args ...any) {
	m := Msg{Key: key}
	if len(args) > 0 {
		m.Args = i18n.Args{}
		for i := 0; i+1 < len(args); i += 2 {
			m.Args[args[i].(string)] = args[i+1]
		}
	}
	errs[field] = m
}

// sshUserRE is a portable account name: lower-case, no shell metacharacters,
// because the account ends up in sshd's and install's command lines.
var sshUserRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidSSHUser reports whether user can be the account Proxier logs in as.
func ValidSSHUser(user string) bool { return sshUserRE.MatchString(user) }

// Defaults is what Settings → Servers starts a new server's form with.
func (s *Service) Defaults(ctx context.Context) (access, setup string) {
	access, err := s.Settings.Get(ctx, conf.DefaultAccessKey)
	if err != nil || access != store.AccessKey {
		access = store.AccessPassword
	}
	setup, err = s.Settings.Get(ctx, conf.DefaultSetupKey)
	if err != nil || setup != store.SetupMinimal {
		setup = store.SetupFull
	}
	return access, setup
}

// check validates the form as far as the database and settings go, all at once.
// withPassword is false for the live summary, which never sees the password.
func (s *Service) check(ctx context.Context, f Form, withPassword bool) (*checked, FieldErrors) {
	errs := FieldErrors{}
	c := &checked{form: f}
	if c.form.SSHPort == 0 {
		c.form.SSHPort = 22
	}
	c.form.IP = strings.TrimSpace(f.IP)
	c.form.SSHIP = strings.TrimSpace(f.SSHIP)
	if c.form.SSHIP != "" {
		ip, err := netip.ParseAddr(c.form.SSHIP)
		if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
			fe(errs, "ssh_ip", "servers.err.ssh_ip")
		} else {
			c.form.SSHIP = ip.Unmap().String()
		}
	}

	// the address
	addr, err := netip.ParseAddr(c.form.IP)
	switch {
	case c.form.IP == "":
		fe(errs, "ip", "servers.err.ip_required")
	case err != nil || !addr.Is4() || addr.Is4In6():
		fe(errs, "ip", "servers.err.ip_v4")
	default:
		c.form.IP = addr.String()
		switch other, ok, qerr := store.HolderOfIP(ctx, s.DB.R, c.form.IP); {
		case qerr != nil || !ok:
		case other.Previous:
			fe(errs, "ip", "servers.err.ip_previous", "name", other.Name)
		default:
			fe(errs, "ip", "servers.err.ip_used", "name", other.Name)
		}
	}
	// The same IP is no separate SSH address: left empty, SSH follows the
	// primary IP through an IP change instead of staying on the old one.
	if c.form.SSHIP == c.form.IP {
		c.form.SSHIP = ""
	}
	if c.form.SSHPort < 1 || c.form.SSHPort > 65535 {
		fe(errs, "ssh_port", "servers.err.ssh_port")
	}

	// how Proxier gets in, and what provisioning does besides the stack
	defAccess, defSetup := s.Defaults(ctx)
	c.form.AccessMode = strings.TrimSpace(f.AccessMode)
	if c.form.AccessMode == "" {
		c.form.AccessMode = defAccess
	}
	switch c.form.AccessMode {
	case store.AccessPassword, store.AccessKey:
	default:
		fe(errs, "access", "servers.err.access_unknown")
	}
	c.form.Setup = strings.TrimSpace(f.Setup)
	if c.form.Setup == "" {
		c.form.Setup = defSetup
	}
	switch c.form.Setup {
	case store.SetupFull, store.SetupMinimal:
	default:
		fe(errs, "setup", "servers.err.setup_unknown")
	}
	c.form.SSHUser = strings.TrimSpace(f.SSHUser)
	if c.form.AccessMode == store.AccessKey {
		if c.form.SSHUser == "" {
			c.form.SSHUser = "root"
		}
		if !ValidSSHUser(c.form.SSHUser) {
			fe(errs, "ssh_user", "servers.err.ssh_user_invalid")
		}
		// Full setup turns root login off, and Proxier has no other account to
		// log in as on a key-access server.
		if c.form.SSHUser == "root" && c.form.Setup == store.SetupFull {
			fe(errs, "setup", "servers.err.root_key_full_setup")
		}
	} else {
		// The proxier user provisioning creates; the form's field is ignored.
		c.form.SSHUser = remote.DeployUser
	}
	if c.form.AccessMode == store.AccessPassword && withPassword && f.RootPassword == "" {
		fe(errs, "root_password", "servers.err.password_required")
	}
	if utf8.RuneCountInString(f.Notes) > 10000 {
		fe(errs, "notes", "servers.err.notes_long")
	}

	// the country
	switch c.form.Country = strings.TrimSpace(f.Country); {
	case c.form.Country == "":
		fe(errs, "country", "servers.err.country_required")
	case !country.Valid(c.form.Country):
		fe(errs, "country", "servers.err.country_unknown")
	default:
		c.prefix = country.Prefix(c.form.Country)
	}

	// the template and its version
	tpl, err := store.GetTemplate(ctx, s.DB.R, f.TemplateID)
	switch {
	case err != nil:
		fe(errs, "template", "servers.err.template_missing")
	case !tpl.ArchivedAt.IsZero():
		fe(errs, "template", "servers.err.template_archived")
	default:
		c.tpl = tpl
		c.version = f.Version
		if c.version == 0 {
			c.version = tpl.DefaultVersion
		}
		if c.version == 0 {
			fe(errs, "template", "servers.err.no_version")
		} else if v, err := s.Templates.Version(ctx, tpl.ID, c.version); err != nil {
			fe(errs, "version", "servers.err.version_missing", "version", c.version)
		} else {
			c.files = v.Files
			m, fs := manifest.Parse(v.Files[manifest.Name])
			if m == nil || !(finding.Report{Findings: fs}).OK() {
				fe(errs, "template", "servers.err.manifest_broken")
			} else {
				c.man = m
			}
		}
	}

	// parameters
	if c.man != nil {
		c.public, c.secret = checkParams(c.man, f.Params, errs)
	}

	// the name, the hostname and Cloudflare
	c.form.Hostname = NormalizeHostname(f.Hostname)
	if c.form.Hostname != "" {
		if render.ValidHostname(c.form.Hostname) != nil {
			fe(errs, "hostname", "servers.err.hostname_invalid")
		} else {
			c.hostname = c.form.Hostname
		}
		if c.prefix != "" {
			if n, err := store.NextNumber(ctx, s.DB.R, c.prefix); err == nil {
				c.number = n
			}
		}
	} else {
		pattern, perr := s.Settings.Get(ctx, conf.HostnamePatternKey)
		if perr == nil && pattern == "" {
			// Without a pattern no country gives a name, so this shows before
			// one is picked.
			fe(errs, "hostname", "servers.err.no_hostname_pattern")
		}
		if c.prefix != "" && perr == nil && pattern != "" {
			n, err := store.NextNumber(ctx, s.DB.R, c.prefix)
			if err == nil {
				c.number = n
				if h, herr := render.Hostname(pattern, c.prefix, n); herr != nil {
					fe(errs, "hostname", "servers.err.hostname_pattern")
				} else {
					c.hostname = h
				}
			}
		}
	}
	if c.hostname != "" {
		// A pattern name held by the server of that very name is a create that
		// committed meanwhile: Create takes the next number in its
		// transaction, and checks again there.
		name, ok, err := store.HolderOfHostname(ctx, s.DB.R, c.hostname)
		if err == nil && ok && (c.form.Hostname != "" || name != fmt.Sprintf("%s-%d", c.prefix, c.number)) {
			fe(errs, "hostname", "servers.err.hostname_used", "host", c.hostname, "name", name)
		}
		s.checkRouting(ctx, c.hostname, errs)
	}
	// A typed hostname outside the Cloudflare zones is the member's to point
	// by hand; a pattern's name must be in one.
	typed := c.form.Hostname != ""
	token, terr := s.Settings.Get(ctx, cloudflare.TokenKey)
	switch noCF := terr == nil && token == ""; {
	case noCF && typed:
		c.manualDNS = c.hostname != ""
	case noCF:
		fe(errs, "dns", "servers.err.no_cloudflare")
	case c.hostname != "":
		switch _, ok, err := s.DNS().Covers(ctx, c.hostname); {
		case errors.Is(err, dns.ErrNotConfigured) && typed, err == nil && !ok && typed:
			c.manualDNS = true
		case errors.Is(err, dns.ErrNotConfigured):
			fe(errs, "dns", "servers.err.no_cloudflare")
		case err != nil:
			fe(errs, "dns", "servers.err.dns_check", "error", err.Error())
		case !ok:
			fe(errs, "dns", "servers.err.not_covered", "host", c.hostname)
		}
	}
	c.form.Addresses = s.checkAddresses(ctx, f.Addresses, c.form.IP, c.hostname, errs)
	if c.manualDNS && !f.SkipDNS && (withPassword || f.CheckDNS) && addr.Is4() && !addr.Is4In6() {
		s.checkManualDNS(ctx, c, errs)
	}
	return c, errs
}

// checkManualDNS checks that the record made by hand points the hostname at
// the IP, and only there.
func (s *Service) checkManualDNS(ctx context.Context, c *checked, errs FieldErrors) {
	found, err := s.Waiter().Check(ctx, c.hostname, c.form.IP)
	switch {
	case err != nil:
		fe(errs, "dns", "servers.err.dns_manual_check", "host", c.hostname, "error", err.Error())
	case !found.OK:
		fe(errs, "dns", "servers.err.dns_manual_missing", "host", c.hostname, "ip", c.form.IP, "said", found.Said)
	default:
		c.found = found.Said
	}
}

// checkAddresses validates the additional address rows and returns the ones
// to store. A row with both fields empty is left out; a half-filled one is a
// refusal. Proxier does not manage their DNS, so no record is looked up.
func (s *Service) checkAddresses(ctx context.Context, rows []FormAddress, primaryIP, primaryHost string, errs FieldErrors) []FormAddress {
	out := make([]FormAddress, 0, len(rows))
	used := NewAddressSet(primaryIP, primaryHost)
	for i, row := range rows {
		ip, host := strings.TrimSpace(row.IP), NormalizeHostname(row.Hostname)
		if ip == "" && host == "" {
			continue
		}
		nip, nhost, rowErrs := CheckAddress(ctx, s.DB.R, 0, used, ip, host)
		rowFieldErrors(errs, i, rowErrs)
		if len(rowErrs) == 0 {
			out = append(out, FormAddress{IP: nip, Hostname: nhost, row: i})
		}
	}
	return out
}

// rowFieldErrors copies a row's refusals under the form's per-row field names.
func rowFieldErrors(errs FieldErrors, row int, rowErrs FieldErrors) {
	if m, bad := rowErrs["ip"]; bad {
		errs[fmt.Sprintf("addr_ip_%d", row)] = m
	}
	if m, bad := rowErrs["hostname"]; bad {
		errs[fmt.Sprintf("addr_host_%d", row)] = m
	}
}

// NormalizeHostname is the hostname as typed in the form, trimmed and in
// lower case, without the root's trailing dot.
func NormalizeHostname(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// checkRouting refuses a hostname a routing list covers: the router would send
// Proxier's checks of the server, and its own connection to it, into the
// tunnel (docs/processes/routing/routing-lists.md#rules).
func (s *Service) checkRouting(ctx context.Context, hostname string, errs FieldErrors) {
	if s.Routing == nil {
		return
	}
	g := s.Routing()
	if g == nil {
		return
	}
	hits, err := g.Covering(ctx, []string{hostname})
	switch {
	case err != nil:
		fe(errs, "routing", "servers.err.routing_check", "error", err.Error())
	case len(hits) > 0:
		r := hits[0]
		fe(errs, "routing", "servers.err.routed", "host", r.Hostname, "domain", r.Domain, "service", r.Service, "list", r.List)
	}
}

// checkParams validates each declared parameter and splits the values into the
// stored public ones and the sealed secret ones. Undeclared values are dropped.
func checkParams(m *manifest.Manifest, given map[string]string, errs FieldErrors) (public, secret map[string]string) {
	public, secret = map[string]string{}, map[string]string{}
	for _, p := range m.Parameters {
		raw := strings.TrimSpace(given[p.Key])
		if raw == "" && p.Default != nil {
			raw = *p.Default
		}
		field := "param." + p.Key
		if raw == "" {
			if p.Required {
				fe(errs, field, "servers.err.param_required", "label", p.Label)
			}
			continue
		}
		if key := paramProblem(p, raw); key != "" {
			fe(errs, field, key, "label", p.Label)
			continue
		}
		if p.Secret {
			secret[p.Key] = raw
		} else {
			public[p.Key] = raw
		}
	}
	return public, secret
}

// paramProblem returns the i18n key of why raw is not a valid value of p.
func paramProblem(p manifest.Parameter, raw string) string { return render.ParamProblem(p, raw) }

// Summarize is the live summary: what Create would make, with the form errors
// that need no network. It never needs the root password. When suggest is
// true it also looks up the IP's country (Suggested), for a picker the admin
// hasn't chosen in.
func (s *Service) Summarize(ctx context.Context, f Form, suggest bool) Summary {
	c, errs := s.check(ctx, f, false)
	sum := Summary{Errors: errs}
	// Blank when SSH goes to the server's IP: the summary shows the row only
	// for a different address.
	sum.SSHIP = c.form.SSHIP
	if suggest {
		if cc := s.country(ctx, c.form.IP); country.Valid(cc) {
			sum.Suggested = cc
		}
	}
	if c.prefix == "" || c.hostname == "" {
		return sum
	}
	sum.Name = fmt.Sprintf("%s-%d", c.prefix, c.number)
	sum.ManagementHostname, sum.ProxyHostname = c.hostname, c.hostname
	sum.DNSRecord = fmt.Sprintf("A %s → %s, DNS only, TTL 60", c.hostname, c.form.IP)
	if c.manualDNS {
		sum.ManualDNS, sum.DNSFound = true, c.found
		sum.DNSRecord = fmt.Sprintf("A %s → %s", c.hostname, c.form.IP)
	}
	for _, a := range c.form.Addresses {
		sum.Addresses = append(sum.Addresses, a.IP+" → "+a.Hostname)
	}
	if c.man != nil {
		// Endpoints bound to addresses the server will not have are left out.
		n := 0
		for _, e := range c.man.Endpoints {
			if e.Address == 0 || e.Address <= 1+len(c.form.Addresses) {
				n++
			}
		}
		many := n > 1
		for _, e := range c.man.Endpoints {
			if e.Address > 0 && e.Address > 1+len(c.form.Addresses) {
				continue
			}
			sum.Endpoints = append(sum.Endpoints, endpoint.DisplayName(flagOf(c.form.Country), shownName(c.form.Country), c.number, e.Key, many))
		}
	}
	return sum
}

// country looks the IP's country up, remembering the answer for ten minutes.
// The lookup is only a suggestion, so a failure is an empty answer.
func (s *Service) country(ctx context.Context, ip string) string {
	if _, err := netip.ParseAddr(ip); err != nil {
		return ""
	}
	g := &s.geo
	g.mu.Lock()
	if e, ok := g.m[ip]; ok && s.now().Sub(e.at) < geoTTL {
		g.mu.Unlock()
		return e.country
	}
	g.mu.Unlock()
	pattern, err := s.Settings.Get(ctx, conf.IPCountryURLKey)
	if err != nil || pattern == "" {
		return ""
	}
	cc := geoip.Lookup(ctx, pattern, ip)
	g.mu.Lock()
	if g.m == nil {
		g.m = map[string]geoEntry{}
	}
	g.m[ip] = geoEntry{country: cc, at: s.now()}
	g.mu.Unlock()
	return cc
}

// jobPayload is the payload of servers.provision. Never secrets.
type jobPayload struct {
	ServerID     int64 `json:"server_id"`
	OverwriteDNS bool  `json:"overwrite_dns,omitempty"`
	// SkipDNS skips the DNS step of a record made by hand: the member checks
	// the record themselves.
	SkipDNS bool `json:"skip_dns,omitempty"`
	// ProxyTest holds the smoke test's timings per endpoint, for the activation.
	ProxyTest map[string]proxyTiming `json:"proxy_test,omitempty"`
}

type proxyTiming struct {
	FirstByteMS int `json:"first_byte_ms"`
	Kbps        int `json:"kbps"`
}

// Create checks the form, reserves the server's name, stores the server and
// its parameters, queues the provisioning job with the root password as a job
// secret, and records server.created, all in one transaction. FieldErrors
// means nothing was created.
func (s *Service) Create(ctx context.Context, f Form, actor string) (int64, error) {
	c, errs := s.check(ctx, f, true)
	if len(errs) > 0 {
		return 0, errs
	}
	var id int64
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		// The number is taken here, in the writing transaction: two creates at
		// once get nl-2 and nl-3, never the same.
		number, err := store.NextNumber(ctx, tx, c.prefix)
		if err != nil {
			return err
		}
		host := c.form.Hostname
		if host == "" {
			pattern, err := s.Settings.Get(ctx, conf.HostnamePatternKey)
			if err != nil {
				return err
			}
			if host, err = render.Hostname(pattern, c.prefix, number); err != nil {
				return err
			}
		}
		// Checked again here: two creates at once must not share a hostname.
		switch name, ok, err := store.HolderOfHostname(ctx, tx, host); {
		case err != nil:
			return err
		case ok:
			return FieldErrors{"hostname": {Key: "servers.err.hostname_used", Args: i18n.Args{"host": host, "name": name}}}
		}
		id, err = store.InsertServer(ctx, tx, store.NewServer{
			Country: c.form.Country, Number: number, Name: fmt.Sprintf("%s-%d", c.prefix, number), IP: c.form.IP, SSHPort: c.form.SSHPort,
			AccessMode: c.form.AccessMode, SSHUser: c.form.SSHUser, Setup: c.form.Setup, SSHIP: c.form.SSHIP,
			ManagementHostname: host, ProxyHostname: host, TemplateID: c.tpl.ID, TemplateVersion: c.version,
			Params: marshalStrings(c.public), Notes: f.Notes, ManualDNS: c.manualDNS,
		}, db.At(s.Now()), func(id int64) []byte { return sealed.SealParams(s.Vault, id, c.secret) })
		if errors.Is(err, store.ErrIPInUse) {
			return FieldErrors{"ip": {Key: "servers.err.ip_used", Args: i18n.Args{"name": "?"}}}
		}
		if err != nil {
			return err
		}
		// Checked again here, where no other create can slip in between the
		// check and the insert; the form's check only gives early field errors.
		used := NewAddressSet(c.form.IP, host)
		for _, a := range c.form.Addresses {
			if _, _, rowErrs := CheckAddress(ctx, tx, id, used, a.IP, a.Hostname); len(rowErrs) > 0 {
				out := FieldErrors{}
				rowFieldErrors(out, a.row, rowErrs)
				return out
			}
			if _, err := store.InsertAddress(ctx, tx, id, a.IP, a.Hostname, db.At(s.Now())); err != nil {
				return err
			}
		}
		secrets := map[string]string{}
		if c.form.AccessMode == store.AccessPassword {
			secrets[rootPasswordSecret] = f.RootPassword
		}
		job, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
			Type: JobType, Payload: jobPayload{ServerID: id, SkipDNS: f.SkipDNS}, Secrets: secrets,
			ResourceKey: "server:" + strconv.FormatInt(id, 10), CreatedBy: actor,
		})
		if err != nil {
			return err
		}
		if err := store.SetProvisionJob(ctx, tx, id, job.ID); err != nil {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Type: "server.created", Subject: store.ServerSubject(id), Actor: actor,
			Payload: map[string]any{"ip": c.form.IP, "ssh_ip": c.form.SSHIP, "country": c.form.Country, "hostname": host, "manual_dns": c.manualDNS, "template_version": c.version, "access": c.form.AccessMode, "setup": c.form.Setup}})
		return err
	})
	if err != nil {
		return 0, err
	}
	s.Jobs.Kick()
	return id, nil
}
