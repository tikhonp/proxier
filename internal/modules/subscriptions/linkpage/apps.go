// Package linkpage is what a browser that opens a link's URL gets: a page
// that gets the person connected, with apps for their device, buttons that
// add the link to an app, a copy button and a QR code
// (docs/integrations/subscription-format.md#browser-page). It is pure: the
// fetch decides when it is served and what it holds.
package linkpage

import (
	"encoding/base64"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
)

// Platform is one tab of the page.
type Platform string

// The platforms, as their tabs and section ids name them.
const (
	IOS     Platform = "ios"
	Android Platform = "android"
	MacOS   Platform = "macos"
	Windows Platform = "windows"
	Linux   Platform = "linux"
	TV      Platform = "tv"
)

// App is a client app that takes the link as a subscription.
type App struct {
	Name string
	// Import is the app's deep link that adds url, named title; nil: the app
	// has none and the person adds the link by hand.
	Import func(url, title string) string
}

// Download is one button to an app's official store or release page.
type Download struct {
	Label string // a message key
	URL   string
}

// Offer is an app as one platform's tab suggests it.
type Offer struct {
	App       *App
	Note      string // a message key, or ""
	Downloads []Download
	Manual    string // the message key of the app's own import, for when there is no deep link or it does nothing
}

// Shelf is one platform's tab: its apps in order, the first recommended.
type Shelf struct {
	Platform Platform
	// DeepLinks: a browser on the platform can open an app's deep link (a TV
	// has no browser to tap one in).
	DeepLinks bool
	Offers    []Offer
}

func raw(scheme string) func(url, title string) string {
	return func(url, _ string) string { return scheme + url }
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// The apps and their deep links. A link URL has no "?" or "#", so a path
// takes it as it is; a query or fragment takes it and the title
// percent-encoded byte by byte (output.Escape: a space is %20, not +).
var (
	happ     = &App{Name: "Happ", Import: raw("happ://add/")}
	v2raytun = &App{Name: "v2RayTun", Import: raw("v2raytun://import/")}
	hiddify  = &App{Name: "Hiddify", Import: func(u, t string) string { return "hiddify://import/" + u + "#" + output.Escape(t) }}
	v2rayng  = &App{Name: "v2rayNG", Import: func(u, t string) string {
		return "v2rayng://install-sub?url=" + output.Escape(u) + "#" + output.Escape(t)
	}}
	v2rayn = &App{Name: "v2rayN"}
	karing = &App{Name: "Karing", Import: func(u, t string) string {
		return "karing://install-config?url=" + output.Escape(u) + "&name=" + output.Escape(t)
	}}
	incy         = &App{Name: "INCY", Import: raw("incy://import/")}
	shadowrocket = &App{Name: "Shadowrocket", Import: func(u, t string) string {
		return "shadowrocket://add/sub://" + b64(u) + "?remark=" + output.Escape(t)
	}}
	streisand = &App{Name: "Streisand", Import: func(u, t string) string { return "streisand://import/" + u + "#" + output.Escape(t) }}
	throne    = &App{Name: "Throne", Import: func(u, t string) string { return "throne://addsub/" + b64(u+"#"+t) }}
)

// Store and release pages.
var (
	happAppStore    = Download{"links.page.dl.appstore", "https://apps.apple.com/us/app/happ-proxy-utility/id6504287215"}
	happTestFlight  = Download{"links.page.dl.testflight", "https://testflight.apple.com/join/ystvnbsP"}
	happAppleTV     = Download{"links.page.dl.appstore_tv", "https://apps.apple.com/us/app/happ-proxy-utility-for-tv/id6748297274"}
	happPlay        = Download{"links.page.dl.play", "https://play.google.com/store/apps/details?id=com.happproxy"}
	happAPK         = Download{"links.page.dl.apk", "https://github.com/Happ-proxy/happ-android/releases/latest/download/Happ.apk"}
	happWindows     = Download{"links.page.dl.exe", "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/setup-Happ.x64.exe"}
	happMac         = Download{"links.page.dl.dmg", "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/Happ.macOS.universal.dmg"}
	happLinux       = Download{"links.page.dl.deb", "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/Happ.linux.x64.deb"}
	v2raytunPlay    = Download{"links.page.dl.play", "https://play.google.com/store/apps/details?id=com.v2raytun.android"}
	hiddifyPlay     = Download{"links.page.dl.play", "https://play.google.com/store/apps/details?id=app.hiddify.com"}
	hiddifyReleases = Download{"links.page.dl.github", "https://github.com/hiddify/hiddify-app/releases/latest"}
	v2rayngReleases = Download{"links.page.dl.github", "https://github.com/2dust/v2rayNG/releases/latest"}
	v2raynReleases  = Download{"links.page.dl.github", "https://github.com/2dust/v2rayN/releases/latest"}
	karingAppStore  = Download{"links.page.dl.appstore", "https://apps.apple.com/us/app/karing/id6472431552"}
	incyAppStore    = Download{"links.page.dl.appstore", "https://apps.apple.com/ru/app/incy/id6756943388"}
	shadowrocketApp = Download{"links.page.dl.appstore", "https://apps.apple.com/us/app/shadowrocket/id932747118"}
	streisandApp    = Download{"links.page.dl.appstore", "https://apps.apple.com/us/app/streisand/id6450534064"}
	throneReleases  = Download{"links.page.dl.github", "https://github.com/throneproj/Throne/releases/latest"}
)

// The manual imports: one loose line each, since the apps' own labels
// weren't tried on devices.
const (
	phoneManual   = "links.page.manual.tap_plus"
	desktopManual = "links.page.manual.click_plus"
	tvManual      = "links.page.manual.tv"
)

// The desktop apps that are the same on macOS, Windows and Linux.
var (
	desktopHiddify = Offer{App: hiddify, Note: "links.page.note.open_source", Downloads: []Download{hiddifyReleases}, Manual: desktopManual}
	desktopV2rayN  = Offer{App: v2rayn, Note: "links.page.note.v2rayn", Downloads: []Download{v2raynReleases}, Manual: "links.page.manual.v2rayn"}
	desktopThrone  = Offer{App: throne, Note: "links.page.note.open_source", Downloads: []Download{throneReleases}, Manual: "links.page.manual.throne"}
)

// Shelves are the tabs in order, each with its apps in order: the one
// table the page is drawn from.
var Shelves = []Shelf{
	{Platform: IOS, DeepLinks: true, Offers: []Offer{
		{App: shadowrocket, Note: "links.page.note.shadowrocket", Downloads: []Download{shadowrocketApp}, Manual: phoneManual},
		{App: karing, Note: "links.page.note.free_ru_store", Downloads: []Download{karingAppStore}, Manual: phoneManual},
		{App: incy, Note: "links.page.note.ru_store", Downloads: []Download{incyAppStore}, Manual: phoneManual},
		{App: happ, Note: "links.page.note.happ_ios", Downloads: []Download{happAppStore, happTestFlight}, Manual: phoneManual},
		{App: streisand, Note: "links.page.note.not_ru_store", Downloads: []Download{streisandApp}, Manual: phoneManual},
	}},
	{Platform: Android, DeepLinks: true, Offers: []Offer{
		{App: happ, Note: "links.page.note.apk", Downloads: []Download{happPlay, happAPK}, Manual: phoneManual},
		{App: v2raytun, Downloads: []Download{v2raytunPlay}, Manual: phoneManual},
		{App: hiddify, Note: "links.page.note.open_source", Downloads: []Download{hiddifyPlay, hiddifyReleases}, Manual: phoneManual},
		{App: v2rayng, Note: "links.page.note.v2rayng", Downloads: []Download{v2rayngReleases}, Manual: phoneManual},
	}},
	{Platform: MacOS, DeepLinks: true, Offers: []Offer{
		{App: happ, Note: "links.page.note.happ_mac", Downloads: []Download{happMac, happAppStore}, Manual: desktopManual},
		desktopHiddify,
		{App: karing, Note: "links.page.note.free_ru_store", Downloads: []Download{karingAppStore}, Manual: desktopManual},
		desktopV2rayN,
	}},
	{Platform: Windows, DeepLinks: true, Offers: []Offer{
		{App: happ, Downloads: []Download{happWindows}, Manual: desktopManual},
		desktopHiddify, desktopV2rayN, desktopThrone,
	}},
	{Platform: Linux, DeepLinks: true, Offers: []Offer{
		{App: happ, Downloads: []Download{happLinux}, Manual: desktopManual},
		desktopHiddify, desktopV2rayN, desktopThrone,
	}},
	{Platform: TV, Offers: []Offer{
		{App: happ, Note: "links.page.note.happ_tv", Downloads: []Download{happAppleTV, happPlay, happAPK}, Manual: tvManual},
		{App: v2rayng, Note: "links.page.note.v2rayng_tv", Downloads: []Download{v2rayngReleases}, Manual: tvManual},
	}},
}
