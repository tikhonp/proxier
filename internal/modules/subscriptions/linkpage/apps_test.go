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

// appNamed is the app the shelves offer under name.
func appNamed(t *testing.T, name string) *App {
	t.Helper()
	for _, sh := range Shelves {
		for _, o := range sh.Offers {
			if o.App.Name == name {
				return o.App
			}
		}
	}
	t.Fatalf("no app %s", name)
	return nil
}

// TestDeepLinksAreExact: a wrong scheme or encoding is a dead button, so
// each app's link is spelled out for a title with a space and Cyrillic.
func TestDeepLinksAreExact(t *testing.T) {
	for name, want := range map[string]string{
		"Happ":         "happ://add/" + testURL,
		"Hiddify":      "hiddify://import/" + testURL + "#" + encTitle,
		"v2rayNG":      "intent://install-sub?url=" + encURL + "#" + encTitle + "#Intent;scheme=v2rayng;package=com.v2ray.ang;S.browser_fallback_url=https%3A%2F%2Fgithub.com%2F2dust%2Fv2rayNG%2Freleases%2Flatest;end",
		"INCY":         "incy://import/" + testURL,
		"Shadowrocket": "shadowrocket://add/sub://aHR0cHM6Ly9wLmV4YW1wbGUuY29tL3MvQWJDLTEyM194eXo=?remark=" + encTitle,
	} {
		app := appNamed(t, name)
		if got := app.Import(testURL, testTitle); !app.HasImport() || got != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
}

// TestAnAppWithoutADeepLink: an app with no import has no Add button and
// renders no link, so the page falls back to its manual import.
func TestAnAppWithoutADeepLink(t *testing.T) {
	shelves, err := load([]byte(strings.Replace(string(appsYAML), `, import: "incy://import/{{.URL}}"}`, "}", 1)))
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, sh := range shelves {
		for _, o := range sh.Offers {
			if o.App.Name != "INCY" {
				continue
			}
			seen = true
			if o.App.HasImport() || o.App.Import(testURL, testTitle) != "" {
				t.Errorf("%s: INCY without an import has a deep link", sh.Platform)
			}
		}
	}
	if !seen {
		t.Error("no INCY on any tab")
	}
}

// TestShelvesFollowTheList: the tabs and their apps in the order the page
// promises, the first of each recommended.
func TestShelvesFollowTheList(t *testing.T) {
	want := map[Platform]string{
		IOS:     "Shadowrocket Hiddify INCY Happ",
		Android: "Hiddify Happ v2rayNG",
		MacOS:   "Shadowrocket Hiddify Happ",
		Windows: "Hiddify Happ",
		Linux:   "Hiddify Happ",
		TV:      "Shadowrocket Happ v2rayNG",
	}
	var order []string
	for _, s := range Shelves {
		order = append(order, string(s.Platform))
		var names []string
		for _, o := range s.Offers {
			names = append(names, o.App.Name)
			if len(o.Downloads) == 0 || o.Manual.RU == "" {
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
	for _, sh := range Shelves {
		if sh.DeepLinks != (sh.Platform != TV) {
			t.Errorf("%s: deep links %v", sh.Platform, sh.DeepLinks)
		}
	}
}

// TestLoadRefusesABadList: a mistake in apps.yaml is named, never a page
// with a dead button or a missing text.
func TestLoadRefusesABadList(t *testing.T) {
	good := string(appsYAML)
	for what, c := range map[string]struct{ old, new, want string }{
		"an unknown app":          {"- app: incy", "- app: nope", "ios, nope: no such app"},
		"an unknown field":        {"  incy: {name: INCY,", "  incy: {nme: INCY,", "field nme not found"},
		"a broken deep link":      {"incy://import/{{.URL}}", "incy://import/{{.Link}}", "app incy: import"},
		"an unknown function":     {"incy://import/{{.URL}}", "incy://import/{{hex .URL}}", "app incy: import"},
		"a note without Russian":  {`ru: "Есть и в российском App Store."`, `ru: ""`, "ios, incy: the note needs both en and ru"},
		"a label without English": {`apk: {en: "APK file",`, `apk: {en: "",`, "apk: needs both en and ru"},
		"an unknown label":        {"{label: testflight,", "{label: tf,", `label "tf": no such label`},
		"an unknown manual":       {"manual: tv", "manual: telly", `manual "telly": no such manual`},
		"a plain http download":   {`"https://testflight.apple.com`, `"http://testflight.apple.com`, "is not an https URL"},
		"a platform twice":        {"- platform: linux", "- platform: windows", "platform windows: twice"},
		"an unknown platform":     {"- platform: tv", "- platform: tvos", `platform "tvos": not one of`},
		"a missing platform":      {"- platform: tv", "- platform: tvos", "platform tv: missing"},
	} {
		if !strings.Contains(good, c.old) {
			t.Fatalf("%s: apps.yaml has no %q", what, c.old)
		}
		_, err := load([]byte(strings.Replace(good, c.old, c.new, 1)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", what, err, c.want)
		}
	}
	if _, err := load(appsYAML); err != nil {
		t.Errorf("apps.yaml: %v", err)
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
