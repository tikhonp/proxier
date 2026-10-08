package fetch

import "strings"

// apps maps user agents to app families, in order: the first family with a
// pattern the user agent contains (any case) wins, so v2rayNG comes before
// v2rayN and every app before Browser
// (docs/integrations/subscription-format.md#app-detection). Extend it as new
// apps appear.
var apps = []struct {
	Family   string
	Patterns []string
}{
	{"Happ", []string{"happ"}},
	{"v2RayTun", []string{"v2raytun"}},
	{"Hiddify", []string{"hiddify"}},
	{"Shadowrocket", []string{"shadowrocket"}},
	{"Streisand", []string{"streisand"}},
	{"v2rayNG", []string{"v2rayng"}},
	{"v2rayN", []string{"v2rayn"}},
	{"FoXray", []string{"foxray"}},
	{"NekoBox / NekoRay", []string{"nekobox", "nekoray"}},
	{"Karing", []string{"karing"}},
	{"Stash", []string{"stash"}},
	{"sing-box", []string{"sing-box", "sfi", "sfa", "sfm"}},
	{"Clash / mihomo", []string{"clash", "mihomo", "meta"}},
	{"Browser", []string{"mozilla"}},
	{"curl / scripts", []string{"curl", "wget", "python", "go-http-client"}},
}

// Detect names the app family of a user agent; "" is Other (pages show the
// user agent itself).
func Detect(userAgent string) string {
	ua := strings.ToLower(userAgent)
	for _, a := range apps {
		for _, p := range a.Patterns {
			if strings.Contains(ua, p) {
				return a.Family
			}
		}
	}
	return ""
}

// MaxUA is how much of a user agent is stored, in characters.
const MaxUA = 256

// TrimUA cuts a user agent to MaxUA characters.
func TrimUA(ua string) string {
	n := 0
	for i := range ua {
		if n == MaxUA {
			return ua[:i]
		}
		n++
	}
	return ua
}
