package config

import (
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"
)

var validKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", MasterKeySize)))

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func minimal() map[string]string {
	return map[string]string{
		"PROXIER_MASTER_KEY": validKey,
		"PROXIER_BASE_URL":   "https://proxier.example.com",
	}
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(minimal()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DataDir != "/data" || c.Listen != ":8080" || c.TSHostname != "proxier" ||
		c.TSControlURL != "https://hs.tikhonnnnn.com" {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if c.TZ.String() != "Europe/Moscow" {
		t.Errorf("TZ = %s", c.TZ)
	}
	if c.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", c.LogLevel)
	}
	if len(c.MasterKey) != MasterKeySize {
		t.Errorf("MasterKey length %d", len(c.MasterKey))
	}
	if c.DatabasePath() != "/data/proxier.db" {
		t.Errorf("DatabasePath = %s", c.DatabasePath())
	}
	if c.TailnetEnabled() {
		t.Error("tailnet enabled without an auth key")
	}
	if c.BaseURL.String() != "https://proxier.example.com" {
		t.Errorf("BaseURL = %s", c.BaseURL)
	}
}

func TestAllErrorsAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PROXIER_TZ":              "Mars/Olympus",
		"PROXIER_LOG_LEVEL":       "loud",
		"PROXIER_TRUSTED_PROXIES": "10.0.0.1, not-an-ip",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"PROXIER_MASTER_KEY", "PROXIER_BASE_URL", "PROXIER_TZ", "PROXIER_LOG_LEVEL", "not-an-ip"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestMasterKey(t *testing.T) {
	for name, key := range map[string]string{
		"not base64": "%%%",
		"too short":  base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"too long":   base64.StdEncoding.EncodeToString(make([]byte, 33)),
	} {
		t.Run(name, func(t *testing.T) {
			m := minimal()
			m["PROXIER_MASTER_KEY"] = key
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), "PROXIER_MASTER_KEY") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), key) && len(key) > 3 {
				t.Error("the error echoes the key")
			}
		})
	}
}

func TestBaseURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://proxier.example.com":       true,
		"https://proxier.example.com/":      true,
		"http://localhost:8080":             true,
		"proxier.example.com":               false,
		"ftp://proxier.example.com":         false,
		"https://proxier.example.com/admin": false,
		"https://u:p@proxier.example.com":   false,
		"https://proxier.example.com/?a=b":  false,
	} {
		m := minimal()
		m["PROXIER_BASE_URL"] = raw
		c, err := Load(env(m))
		if ok != (err == nil) {
			t.Errorf("%s: err = %v", raw, err)
		}
		if ok && strings.HasSuffix(c.BaseURL.String(), "/") {
			t.Errorf("%s: BaseURL keeps a trailing slash: %s", raw, c.BaseURL)
		}
	}
}

func TestTrustedProxies(t *testing.T) {
	m := minimal()
	m["PROXIER_TRUSTED_PROXIES"] = "172.18.0.5, 10.0.0.0/8 ,::1,"
	c, err := Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, p := range c.TrustedProxies {
		got = append(got, p.String())
	}
	if strings.Join(got, " ") != "172.18.0.5/32 10.0.0.0/8 ::1/128" {
		t.Errorf("TrustedProxies = %v", got)
	}
}

func TestWarnings(t *testing.T) {
	m := minimal()
	m["PROXIER_BASE_URL"] = "http://localhost:8080"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(c.Warnings(), "\n")
	for _, want := range []string{"not https", "PROXIER_TRUSTED_PROXIES", "PROXIER_TS_AUTHKEY", "PROXIER_CHROMIUM_URL"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings miss %s:\n%s", want, w)
		}
	}
}
