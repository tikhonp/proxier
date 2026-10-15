package linkpage

import "strings"

// platforms maps user agents to the tab that opens, in order: the first
// platform with a pattern the user agent contains (any case) wins. A TV
// comes first (Android and Tizen TVs say Linux or Android too), iOS before
// macOS ("like Mac OS X"), Android before Linux. iPadOS asks for desktop
// pages as a Mac; the page's script corrects that by touch.
var platforms = []struct {
	Platform Platform
	Patterns []string
}{
	{TV, []string{"smart-tv", "smarttv", "tizen", "web0s", "webos", "googletv", "android tv", "bravia", "appletv", "; aft"}},
	{IOS, []string{"iphone", "ipad", "ipod"}},
	{Android, []string{"android", "cros"}}, // a Chromebook installs Android apps
	{Windows, []string{"windows"}},
	{MacOS, []string{"macintosh", "mac os x"}},
	{Linux, []string{"linux", "x11"}},
}

// PlatformOf is the tab a user agent opens on: iPhone & iPad, the first,
// for one that names no platform.
func PlatformOf(userAgent string) Platform {
	ua := strings.ToLower(userAgent)
	for _, p := range platforms {
		for _, pat := range p.Patterns {
			if strings.Contains(ua, pat) {
				return p.Platform
			}
		}
	}
	return Shelves[0].Platform
}
