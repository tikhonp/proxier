// Package conf declares the subscriptions module's settings section and its
// keys, so the services and the pages read them by the same names.
package conf

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Keys of section subscriptions.
const (
	LinkLanguage      = "subscriptions.link_language"
	ExpiryWarning     = "subscriptions.expiry_warning"
	AlertNetworks     = "subscriptions.alert_networks"
	AlertApps         = "subscriptions.alert_apps"
	NetworkCountryURL = "subscriptions.network_country_url"
	Tombstone         = "subscriptions.tombstone"
	FetchRetention    = "subscriptions.fetch_retention"
)

const day = 24 * time.Hour

// Section is the module's settings section (Settings → Subscriptions).
var Section = settings.Section{
	Name: "subscriptions", Module: "subscriptions",
	Fields: []settings.Field{
		{Key: LinkLanguage, Kind: settings.Enum, Default: "ru", Options: []string{"en", "ru"}},
		{Key: ExpiryWarning, Kind: settings.Duration, Default: "72h0m0s", Min: int64(time.Hour), Max: int64(30 * day)},
		{Key: AlertNetworks, Kind: settings.Int, Default: "4", Min: 1, Max: 1000},
		{Key: AlertApps, Kind: settings.Int, Default: "3", Min: 1, Max: 1000},
		// Empty turns country lookups off.
		{Key: NetworkCountryURL, Kind: settings.String, Default: "https://ipinfo.io/{ip}/country", MaxLen: 300,
			Validate: func(v string) error {
				if v == "" {
					return nil
				}
				u, err := url.Parse(v)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return errors.New("must be an http:// or https:// address")
				}
				if !strings.Contains(v, "{ip}") {
					return errors.New("must contain {ip}, where the network's first address goes")
				}
				return nil
			}},
		{Key: Tombstone, Kind: settings.Duration, Default: "720h0m0s", Min: int64(day), Max: int64(365 * day)},
		// At least a week: the link page counts the last 7 days.
		{Key: FetchRetention, Kind: settings.Duration, Default: "2160h0m0s", Min: int64(7 * day), Max: int64(365 * day)},
	},
}
