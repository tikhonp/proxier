// Package provision builds a server from a template: the new-server form with
// its checks and live summary, Create, and the servers.provision job with its
// retry and Activate anyway (docs/processes/servers/server-provisioning.md).
package provision

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/geoip"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Form is what the new-server form posts.
type Form struct {
	IP, RootPassword string
	SSHPort          int // 0 = 22
	LocationID       int64
	TemplateID       int64
	Version          int // 0 = the template's default version
	Params           map[string]string
	Notes            string
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
	// DNSRecord is the record that will be written: "A <name> → <ip>, DNS only, TTL 60".
	DNSRecord string
	Endpoints []string // display names
	// SuggestedLocation is the location preselected from the IP's country, 0
	// for none; SuggestedCountry is that country.
	SuggestedLocation int64
	SuggestedCountry  string
	Errors            FieldErrors
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
	loc      store.Location
	tpl      store.Template
	version  int
	files    map[string][]byte
	man      *manifest.Manifest
	number   int
	hostname string
	public   map[string]string // non-secret parameters, as stored
	secret   map[string]string
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

// check validates the form as far as the database and settings go, all at once.
// withPassword is false for the live summary, which never sees the password.
func (s *Service) check(ctx context.Context, f Form, withPassword bool) (*checked, FieldErrors) {
	errs := FieldErrors{}
	c := &checked{form: f}
	if c.form.SSHPort == 0 {
		c.form.SSHPort = 22
	}
	c.form.IP = strings.TrimSpace(f.IP)

	// the address
	addr, err := netip.ParseAddr(c.form.IP)
	switch {
	case c.form.IP == "":
		fe(errs, "ip", "servers.err.ip_required")
	case err != nil || !addr.Is4() || addr.Is4In6():
		fe(errs, "ip", "servers.err.ip_v4")
	default:
		c.form.IP = addr.String()
		if other, ok, qerr := store.ServerByIP(ctx, s.DB.R, c.form.IP); qerr == nil && ok {
			fe(errs, "ip", "servers.err.ip_used", "name", other.Name)
		}
	}
	if c.form.SSHPort < 1 || c.form.SSHPort > 65535 {
		fe(errs, "ssh_port", "servers.err.ssh_port")
	}
	if withPassword && f.RootPassword == "" {
		fe(errs, "root_password", "servers.err.password_required")
	}
	if utf8.RuneCountInString(f.Notes) > 10000 {
		fe(errs, "notes", "servers.err.notes_long")
	}

	// the location
	if f.LocationID == 0 {
		fe(errs, "location", "servers.err.location_required")
	} else if loc, err := s.Store.Location(ctx, f.LocationID); err != nil {
		fe(errs, "location", "servers.err.location_missing")
	} else {
		c.loc = loc
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
	if c.loc.ID != 0 {
		n, err := store.NextNumber(ctx, s.DB.R, c.loc.ID)
		if err == nil {
			c.number = n
			pattern, perr := s.Settings.Get(ctx, conf.HostnamePatternKey)
			if perr == nil {
				if h, herr := render.Hostname(pattern, c.loc.Code, n); herr != nil {
					fe(errs, "hostname", "servers.err.hostname_pattern")
				} else {
					c.hostname = h
				}
			}
		}
	}
	if c.hostname != "" {
		s.checkRouting(ctx, c.hostname, errs)
	}
	if token, err := s.Settings.Get(ctx, cloudflare.TokenKey); err == nil && token == "" {
		fe(errs, "dns", "servers.err.no_cloudflare")
	} else if c.hostname != "" {
		switch _, ok, err := s.DNS().Covers(ctx, c.hostname); {
		case errors.Is(err, dns.ErrNotConfigured):
			fe(errs, "dns", "servers.err.no_cloudflare")
		case err != nil:
			fe(errs, "dns", "servers.err.dns_check", "error", err.Error())
		case !ok:
			fe(errs, "dns", "servers.err.not_covered", "host", c.hostname)
		}
	}
	return c, errs
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
// that need no network. It never needs the root password. When locationTouched
// is false and the IP's country matches a location, it suggests that one.
func (s *Service) Summarize(ctx context.Context, f Form, locationTouched bool) Summary {
	c, errs := s.check(ctx, f, false)
	sum := Summary{Errors: errs}
	if !locationTouched {
		if cc := s.country(ctx, c.form.IP); cc != "" {
			if locs, err := s.Store.Locations(ctx); err == nil {
				for _, l := range locs { // by code, so the first of several wins
					if l.Country == cc {
						sum.SuggestedLocation, sum.SuggestedCountry = l.ID, cc
						break
					}
				}
			}
		}
	}
	if c.loc.ID == 0 || c.hostname == "" {
		return sum
	}
	sum.Name = fmt.Sprintf("%s-%d", c.loc.Code, c.number)
	sum.ManagementHostname, sum.ProxyHostname = c.hostname, c.hostname
	sum.DNSRecord = fmt.Sprintf("A %s → %s, DNS only, TTL 60", c.hostname, c.form.IP)
	if c.man != nil {
		many := len(c.man.Endpoints) > 1
		for _, e := range c.man.Endpoints {
			sum.Endpoints = append(sum.Endpoints, endpoint.DisplayName(country.Flag(c.loc.Country), c.loc.Name, c.number, e.Key, many))
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
		number, err := store.ReserveNumber(ctx, tx, c.loc.ID)
		if err != nil {
			return err
		}
		pattern, err := s.Settings.Get(ctx, conf.HostnamePatternKey)
		if err != nil {
			return err
		}
		host, err := render.Hostname(pattern, c.loc.Code, number)
		if err != nil {
			return err
		}
		id, err = store.InsertServer(ctx, tx, store.NewServer{
			LocationID: c.loc.ID, Number: number, Name: fmt.Sprintf("%s-%d", c.loc.Code, number), IP: c.form.IP, SSHPort: c.form.SSHPort,
			ManagementHostname: host, ProxyHostname: host, TemplateID: c.tpl.ID, TemplateVersion: c.version,
			Params: marshalStrings(c.public), Notes: f.Notes,
		}, db.At(s.Now()), func(id int64) []byte { return sealed.SealParams(s.Vault, id, c.secret) })
		if errors.Is(err, store.ErrIPInUse) {
			return FieldErrors{"ip": {Key: "servers.err.ip_used", Args: i18n.Args{"name": "?"}}}
		}
		if err != nil {
			return err
		}
		job, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
			Type: JobType, Payload: jobPayload{ServerID: id}, Secrets: map[string]string{rootPasswordSecret: f.RootPassword},
			ResourceKey: "server:" + strconv.FormatInt(id, 10), CreatedBy: actor,
		})
		if err != nil {
			return err
		}
		if err := store.SetProvisionJob(ctx, tx, id, job.ID); err != nil {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Type: "server.created", Subject: store.ServerSubject(id), Actor: actor,
			Payload: map[string]any{"ip": c.form.IP, "template_version": c.version}})
		return err
	})
	if err != nil {
		return 0, err
	}
	s.Jobs.Kick()
	return id, nil
}
