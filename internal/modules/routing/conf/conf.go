// Package conf declares the routing module's settings section and its keys,
// so the services and the pages read them by the same names.
package conf

import (
	"errors"
	"regexp"
	"time"

	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Keys of section routing.
const (
	RefreshAt   = "routing.refresh_at"
	CatalogAt   = "routing.catalog_at"
	ShrinkMin   = "routing.shrink_min"
	ShrinkPct   = "routing.shrink_pct"
	GitHubToken = "routing.github_token"
	SyncDelay   = "routing.sync_delay"
	DriftEvery  = "routing.drift_every"
	DriftRepair = "routing.drift_repair"
)

var clock = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// validClock accepts a time of day as HH:MM.
func validClock(v string) error {
	if !clock.MatchString(v) {
		return errors.New("must be a time of day as HH:MM, like 04:00")
	}
	return nil
}

// Section is the module's settings section (Settings → Routing, from 3c).
var Section = settings.Section{
	Name: "routing", Module: "routing",
	Fields: []settings.Field{
		{Key: RefreshAt, Kind: settings.String, Default: "04:00", MaxLen: 5, Validate: validClock},
		{Key: CatalogAt, Kind: settings.String, Default: "04:30", MaxLen: 5, Validate: validClock},
		{Key: ShrinkMin, Kind: settings.Int, Default: "20", Min: 1, Max: 100000},
		{Key: ShrinkPct, Kind: settings.Int, Default: "50", Min: 1, Max: 99},
		{Key: GitHubToken, Kind: settings.Secret, Default: "", MaxLen: 200},
		{Key: SyncDelay, Kind: settings.Duration, Default: "30s", Min: 0, Max: int64(10 * time.Minute)},
		{Key: DriftEvery, Kind: settings.Duration, Default: "6h0m0s", Min: int64(time.Hour), Max: int64(168 * time.Hour)},
		{Key: DriftRepair, Kind: settings.Bool, Default: "true"},
	},
}
