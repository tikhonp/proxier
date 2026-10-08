package fetch

import (
	"net/netip"
	"testing"
)

func TestDetectApp(t *testing.T) {
	for ua, want := range map[string]string{
		"Happ/3.2.1/ios CFNetwork/1568.100.1 Darwin/24.0.0":         "Happ",
		"v2raytun/android 5.12.9":                                   "v2RayTun",
		"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box": "Hiddify",
		"Shadowrocket/2370 CFNetwork/1498.700.2 Darwin/23.6.0":      "Shadowrocket",
		"Streisand/2.3 CFNetwork/1568 Darwin/24.0.0":                "Streisand",
		"v2rayNG/1.8.5": "v2rayNG",
		"v2rayN/6.42":   "v2rayN",
		"FoXray/2.6 CFNetwork/1498 Darwin/23.6.0":         "FoXray",
		"NekoBox/Android/1.3.1 (Prefer ClashMeta Format)": "NekoBox / NekoRay",
		"nekoray/3.26":                        "NekoBox / NekoRay",
		"karing/1.0.30.399 android":           "Karing",
		"Stash/2.6.0 Clash/1.9.0":             "Stash",
		"SFI/1.9.3 (Build 1; sing-box 1.9.3)": "sing-box",
		"sing-box 1.10.1":                     "sing-box",
		"Mozilla/5.0 (Windows NT 10.0) Clash": "Clash / mihomo",
		"mihomo/1.18.5":                       "Clash / mihomo",
		"ClashMetaForAndroid/2.10.1.Meta":     "Clash / mihomo",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36": "Browser",
		"curl/8.4.0":             "curl / scripts",
		"Wget/1.21.4":            "curl / scripts",
		"python-requests/2.31.0": "curl / scripts",
		"Go-http-client/2.0":     "curl / scripts",
		"okhttp/4.12":            "",
		"":                       "",
	} {
		if got := Detect(ua); got != want {
			t.Errorf("%q → %q, want %q", ua, got, want)
		}
	}
	if got := TrimUA(string(make([]rune, 300))); len([]rune(got)) != MaxUA {
		t.Errorf("TrimUA kept %d", len([]rune(got)))
	}
}

func TestNetwork(t *testing.T) {
	for ip, want := range map[string]string{
		"198.51.100.23":       "198.51.100.0/24",
		"::ffff:198.51.100.7": "198.51.100.0/24",
		"2001:db8:4f2:1::7":   "2001:db8:4f2::/48",
		"2001:db8::1":         "2001:db8::/48",
	} {
		if got := Network(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s → %q, want %q", ip, got, want)
		}
	}
	if Network(netip.Addr{}) != "" {
		t.Error("an invalid address has a network")
	}
}
