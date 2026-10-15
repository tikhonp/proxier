package linkpage

import (
	"strings"
	"testing"
)

const (
	testURL   = "https://p.example.com/s/AbC-123_xyz"
	testTitle = "Семья мама"
	encTitle  = "%D0%A1%D0%B5%D0%BC%D1%8C%D1%8F%20%D0%BC%D0%B0%D0%BC%D0%B0"
	encURL    = "https%3A%2F%2Fp.example.com%2Fs%2FAbC-123_xyz"
)

// TestDeepLinksAreExact: a wrong scheme or encoding is a dead button, so
// each app's link is spelled out for a title with a space and Cyrillic.
func TestDeepLinksAreExact(t *testing.T) {
	for app, want := range map[*App]string{
		happ:         "happ://add/" + testURL,
		v2raytun:     "v2raytun://import/" + testURL,
		hiddify:      "hiddify://import/" + testURL + "#" + encTitle,
		v2rayng:      "v2rayng://install-sub?url=" + encURL + "#" + encTitle,
		karing:       "karing://install-config?url=" + encURL + "&name=" + encTitle,
		incy:         "incy://import/" + testURL,
		shadowrocket: "shadowrocket://add/sub://aHR0cHM6Ly9wLmV4YW1wbGUuY29tL3MvQWJDLTEyM194eXo=?remark=" + encTitle,
		streisand:    "streisand://import/" + testURL + "#" + encTitle,
		throne:       "throne://addsub/aHR0cHM6Ly9wLmV4YW1wbGUuY29tL3MvQWJDLTEyM194eXoj0KHQtdC80YzRjyDQvNCw0LzQsA==",
	} {
		if got := app.Import(testURL, testTitle); got != want {
			t.Errorf("%s:\n got %s\nwant %s", app.Name, got, want)
		}
	}
	if v2rayn.Import != nil {
		t.Error("v2rayN has no deep link")
	}
}

// TestShelvesFollowTheList: the tabs and their apps in the order the page
// promises, the first of each recommended.
func TestShelvesFollowTheList(t *testing.T) {
	want := map[Platform]string{
		IOS:     "Shadowrocket Karing INCY Happ Streisand",
		Android: "Happ v2RayTun Hiddify v2rayNG",
		MacOS:   "Happ Hiddify Karing v2rayN",
		Windows: "Happ Hiddify v2rayN Throne",
		Linux:   "Happ Hiddify v2rayN Throne",
		TV:      "Happ v2rayNG",
	}
	var order []string
	for _, s := range Shelves {
		order = append(order, string(s.Platform))
		var names []string
		for _, o := range s.Offers {
			names = append(names, o.App.Name)
			if len(o.Downloads) == 0 || o.Manual == "" {
				t.Errorf("%s on %s: no download or no manual import", o.App.Name, s.Platform)
			}
			for _, d := range o.Downloads {
				if !strings.HasPrefix(d.URL, "https://") {
					t.Errorf("%s on %s: %s", o.App.Name, s.Platform, d.URL)
				}
			}
		}
		if got := strings.Join(names, " "); got != want[s.Platform] {
			t.Errorf("%s: %s, want %s", s.Platform, got, want[s.Platform])
		}
	}
	if got := strings.Join(order, " "); got != "ios android macos windows linux tv" {
		t.Errorf("tabs %s", got)
	}
}

func TestPlatformFollowsTheUserAgent(t *testing.T) {
	for ua, want := range map[string]Platform{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1": IOS,
		"Mozilla/5.0 (iPad; CPU OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/124.0 Mobile/15E148 Safari/604.1":           IOS,
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36":                   Android,
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36":                         Windows,
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15":                   MacOS,
		"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0":                                                                  Linux,
		"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0":                                                          Linux,
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36":                          Android,
		"Mozilla/5.0 (SMART-TV; Linux; Tizen 6.0) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/4.0 Chrome/76.0 TV Safari/537.36":         TV,
		"Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0 Safari/537.36 WebAppManager":                       TV,
		"Mozilla/5.0 (Linux; Android 9; AFTMM Build/PS7633) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36":             TV,
		"Mozilla/5.0 (Linux; Android 12; BRAVIA 4K VH2 Build/STT1.211025.001) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36":  TV,
		"Mozilla/5.0": IOS,
		"":            IOS,
	} {
		if got := PlatformOf(ua); got != want {
			t.Errorf("%q: %s, want %s", ua, got, want)
		}
	}
}
