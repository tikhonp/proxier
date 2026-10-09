package discovery

import (
	"net/netip"

	"github.com/tikhonp/proxier/internal/modules/routing/domain"
)

// Class is what kind of host a run saw.
type Class string

// The classes.
const (
	FirstParty Class = "first-party"
	CDN        Class = "cdn"
	Tracker    Class = "tracker"
	ThirdParty Class = "third-party"
	IP         Class = "ip"
)

// cdnSuffixes are CDN and platform infrastructure: shared by many sites, so
// routing them is rarely what the admin wants.
var cdnSuffixes = []string{
	"cloudflare.com", "cloudflare.net", "cloudfront.net", "akamai.net", "akamaihd.net", "akamaized.net",
	"edgekey.net", "edgesuite.net", "fastly.net", "fastly.com", "fastlylb.net", "gstatic.com",
	"googleapis.com", "googleusercontent.com", "ggpht.com", "jsdelivr.net", "unpkg.com", "bootstrapcdn.com",
	"azureedge.net", "azurefd.net", "msecnd.net", "b-cdn.net", "cdn77.org", "amazonaws.com",
	"yastatic.net", "recaptcha.net", "hcaptcha.com",
}

// trackerSuffixes are analytics, tag managers and ad networks.
var trackerSuffixes = []string{
	"google-analytics.com", "googletagmanager.com", "googlesyndication.com", "googleadservices.com",
	"doubleclick.net", "facebook.net", "hotjar.com", "segment.io", "segment.com", "mixpanel.com",
	"amplitude.com", "sentry.io", "nr-data.net", "clarity.ms", "bat.bing.com", "cloudflareinsights.com",
	"mc.yandex.ru", "mc.yandex.com", "top-fwz1.mail.ru", "counter.yadro.ru", "scorecardresearch.com",
	"criteo.com", "criteo.net", "adnxs.com", "taboola.com", "outbrain.com",
}

// Classify says what a host is for a run of registrable: an IP literal, the
// site's own, a tracker, a CDN, or another site's. The site's own wins over
// the built-in lists (a run of cloudflare.com finds first-party hosts).
func Classify(host, registrable string) Class {
	if _, err := netip.ParseAddr(host); err == nil {
		return IP
	}
	switch {
	case domain.Covers(registrable, host):
		return FirstParty
	case under(host, trackerSuffixes):
		return Tracker
	case under(host, cdnSuffixes):
		return CDN
	}
	return ThirdParty
}

func under(host string, suffixes []string) bool {
	for _, s := range suffixes {
		if domain.Covers(s, host) {
			return true
		}
	}
	return false
}
