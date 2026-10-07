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
)

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
