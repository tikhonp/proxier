package auth

import "strings"

// BrowserName turns a user agent into "Chrome on macOS". An unknown browser
// reads "Unknown browser"; an unknown system is left out.
func BrowserName(ua string) string {
	browser := "Unknown browser"
	switch {
	case strings.Contains(ua, "Edg/") || strings.Contains(ua, "EdgA/") || strings.Contains(ua, "EdgiOS/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		browser = "Safari"
	case strings.HasPrefix(ua, "curl/"):
		browser = "curl"
	}
	system := ""
	switch {
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		system = "iOS"
	case strings.Contains(ua, "Android"):
		system = "Android"
	case strings.Contains(ua, "Windows"):
		system = "Windows"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		system = "macOS"
	case strings.Contains(ua, "CrOS"):
		system = "ChromeOS"
	case strings.Contains(ua, "Linux") || strings.Contains(ua, "X11"):
		system = "Linux"
	}
	if system == "" {
		return browser
	}
	return browser + " on " + system
}
