package servers

import (
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// HostnamePatternKey is the setting that names servers' hostnames.
const HostnamePatternKey = "servers.hostname_pattern"

// Section is the module's settings section. Later sub-phases add fields.
var Section = settings.Section{
	Name: "servers", Module: modName,
	Fields: []settings.Field{
		{Key: HostnamePatternKey, Kind: settings.String, Default: "{location}-{number}.hosts.tikhonnnnn.com", MaxLen: 200,
			Validate: func(v string) error {
				// It must make a valid DNS name for any server; xx-1 stands for them.
				_, err := render.Hostname(v, "xx", 1)
				return err
			}},
	},
}
