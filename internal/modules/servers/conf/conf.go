// Package conf declares the servers module's settings section and its keys,
// so the pages and the services read them by the same names.
package conf

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Keys of section servers.
const (
	HostnamePatternKey = "servers.hostname_pattern"
	IPCountryURLKey    = "servers.ip_country_url"
	ProxyTestURLKey    = "servers.proxy_test_url"
	ProxyTestTimeout   = "servers.proxy_test_timeout"
	ProxyTestStall     = "servers.proxy_test_stall"

	SelfcheckEvery        = "servers.selfcheck_every"
	ExternalEvery         = "servers.external_every"
	ExternalOnDemandAfter = "servers.external_on_demand_after"
	ExternalHourlyCap     = "servers.external_hourly_cap"
	ExternalNodesRU       = "servers.external_nodes_ru"
	ExternalNodesAbroad   = "servers.external_nodes_abroad"
	ReferenceDomestic     = "servers.reference_domestic"
	ReferenceForeign      = "servers.reference_foreign"
	FlapConfirmations     = "servers.flap_confirmations"
	SlowFirstByte         = "servers.slow_first_byte"
	SlowKbps              = "servers.slow_kbps"
	CertWarnDays          = "servers.cert_warn_days"
	DiskWarnPct           = "servers.disk_warn_pct"
	DiskFailPct           = "servers.disk_fail_pct"
	ReminderEvery         = "servers.reminder_every"
)

// urlList checks a comma-separated list of http(s) addresses.
func urlList(v string) error {
	n := 0
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		u, err := url.Parse(p)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("every address must be http:// or https://")
		}
		n++
	}
	if n == 0 {
		return errors.New("needs at least one address")
	}
	return nil
}

// nodeList checks a comma-separated list of check-host node names.
func nodeList(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" && !strings.HasSuffix(p, ".node.check-host.net") {
			return errors.New("node names look like ru1.node.check-host.net")
		}
	}
	return nil
}

// Section is the module's settings section. Later sub-phases add fields.
var Section = settings.Section{
	Name: "servers", Module: "servers",
	Fields: []settings.Field{
		{Key: HostnamePatternKey, Kind: settings.String, Default: "{location}-{number}.hosts.tikhonnnnn.com", MaxLen: 200,
			Validate: func(v string) error {
				// It must make a valid DNS name for any server; xx-1 stands for them.
				_, err := render.Hostname(v, "xx", 1)
				return err
			}},
		// Empty turns the lookup off. The answer only preselects a location.
		{Key: IPCountryURLKey, Kind: settings.String, Default: "https://ipinfo.io/{ip}/country", MaxLen: 300,
			Validate: func(v string) error {
				if v == "" {
					return nil
				}
				u, err := url.Parse(v)
				if err != nil || u.Scheme != "https" || u.Host == "" {
					return errors.New("must be an https:// address")
				}
				if !strings.Contains(v, "{ip}") {
					return errors.New("must contain {ip}, where the server's address goes")
				}
				return nil
			}},
		{Key: ProxyTestURLKey, Kind: settings.String, Default: "https://speed.cloudflare.com/__down?bytes=262144", MaxLen: 500,
			Validate: func(v string) error {
				u, err := url.Parse(v)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return errors.New("must be an http:// or https:// address")
				}
				return nil
			}},
		{Key: ProxyTestTimeout, Kind: settings.Duration, Default: "15s", Min: int64(5 * time.Second), Max: int64(2 * time.Minute)},
		{Key: ProxyTestStall, Kind: settings.Duration, Default: "5s", Min: int64(time.Second), Max: int64(time.Minute)},

		// Health (1f).
		{Key: SelfcheckEvery, Kind: settings.Duration, Default: "5m0s", Min: int64(time.Minute), Max: int64(time.Hour)},
		{Key: ExternalEvery, Kind: settings.Duration, Default: "30m0s", Min: int64(5 * time.Minute), Max: int64(24 * time.Hour)},
		{Key: ExternalOnDemandAfter, Kind: settings.Duration, Default: "10m0s", Min: int64(time.Minute), Max: int64(6 * time.Hour)},
		{Key: ExternalHourlyCap, Kind: settings.Int, Default: "60", Min: 0, Max: 1000},
		{Key: ExternalNodesRU, Kind: settings.String, Default: "", MaxLen: 500, Validate: nodeList},
		{Key: ExternalNodesAbroad, Kind: settings.String, Default: "", MaxLen: 500, Validate: nodeList},
		{Key: ReferenceDomestic, Kind: settings.String, Default: "https://ya.ru/,https://vk.com/", MaxLen: 500, Validate: urlList},
		{Key: ReferenceForeign, Kind: settings.String, Default: "https://www.cloudflare.com/cdn-cgi/trace,https://www.google.com/generate_204", MaxLen: 500, Validate: urlList},
		{Key: FlapConfirmations, Kind: settings.Int, Default: "2", Min: 1, Max: 5},
		{Key: SlowFirstByte, Kind: settings.Duration, Default: "2s", Min: int64(100 * time.Millisecond), Max: int64(30 * time.Second)},
		{Key: SlowKbps, Kind: settings.Int, Default: "1000", Min: 1, Max: 1000000},
		{Key: CertWarnDays, Kind: settings.Int, Default: "14", Min: 1, Max: 60},
		{Key: DiskWarnPct, Kind: settings.Int, Default: "10", Min: 1, Max: 50},
		{Key: DiskFailPct, Kind: settings.Int, Default: "2", Min: 1, Max: 20},
		{Key: ReminderEvery, Kind: settings.Duration, Default: "24h0m0s", Min: int64(time.Hour), Max: int64(7 * 24 * time.Hour)},
	},
}

// ProxyTest reads how a proxy test runs: the URL (the template's own when it
// has one, else the setting), the overall timeout and the stall limit.
func ProxyTest(ctx context.Context, st *settings.Store, templateURL string) (url string, timeout, stall time.Duration, err error) {
	url = templateURL
	if url == "" {
		if url, err = st.Get(ctx, ProxyTestURLKey); err != nil {
			return "", 0, 0, err
		}
	}
	if timeout, err = st.GetDuration(ctx, ProxyTestTimeout); err != nil {
		return "", 0, 0, err
	}
	if stall, err = st.GetDuration(ctx, ProxyTestStall); err != nil {
		return "", 0, 0, err
	}
	return url, timeout, stall, nil
}
