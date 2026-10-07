// Package config reads Proxier's environment: what is needed before the
// database is readable, plus what is easier to keep in the secrets submodule.
// Everything else lives in Settings. The variables are listed in
// docs/architecture.md#configuration.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MasterKeySize is the length of the decoded PROXIER_MASTER_KEY.
const MasterKeySize = 32

// Config is the parsed environment. MasterKey is secret: never log a Config.
type Config struct {
	DataDir        string
	MasterKey      []byte
	Listen         string
	BaseURL        *url.URL
	TrustedProxies []netip.Prefix

	TSControlURL string
	TSAuthKey    string
	TSHostname   string

	ChromiumURL string

	TZ       *time.Location
	LogLevel slog.Level
}

// DatabasePath is the SQLite file inside the data directory.
func (c *Config) DatabasePath() string { return filepath.Join(c.DataDir, "proxier.db") }

// TailnetEnabled reports whether a pre-auth key was given.
func (c *Config) TailnetEnabled() bool { return c.TSAuthKey != "" }

// LoadFromEnv reads the process environment.
func LoadFromEnv() (*Config, error) { return Load(os.Getenv) }

// Load parses the variables returned by getenv. It reports every problem at
// once, so a broken deployment is fixed in one round.
func Load(getenv func(string) string) (*Config, error) {
	var errs []error
	get := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}

	c := &Config{
		DataDir:      get("PROXIER_DATA_DIR", "/data"),
		Listen:       get("PROXIER_LISTEN", ":8080"),
		TSControlURL: get("PROXIER_TS_CONTROL_URL", "https://hs.tikhonnnnn.com"),
		TSAuthKey:    get("PROXIER_TS_AUTHKEY", ""),
		TSHostname:   get("PROXIER_TS_HOSTNAME", "proxier"),
		ChromiumURL:  get("PROXIER_CHROMIUM_URL", ""),
	}

	if key := get("PROXIER_MASTER_KEY", ""); key == "" {
		errs = append(errs, errors.New("PROXIER_MASTER_KEY is required (openssl rand -base64 32)"))
	} else if b, err := base64.StdEncoding.DecodeString(key); err != nil {
		errs = append(errs, errors.New("PROXIER_MASTER_KEY is not valid base64"))
	} else if len(b) != MasterKeySize {
		errs = append(errs, fmt.Errorf("PROXIER_MASTER_KEY must decode to %d bytes, got %d", MasterKeySize, len(b)))
	} else {
		c.MasterKey = b
	}

	if raw := get("PROXIER_BASE_URL", ""); raw == "" {
		errs = append(errs, errors.New("PROXIER_BASE_URL is required, e.g. https://proxier.tikhonnnnn.com"))
	} else if u, err := parseBaseURL(raw); err != nil {
		errs = append(errs, fmt.Errorf("PROXIER_BASE_URL: %w", err))
	} else {
		c.BaseURL = u
	}

	if raw := get("PROXIER_TRUSTED_PROXIES", ""); raw != "" {
		for item := range strings.SplitSeq(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			p, err := parsePrefix(item)
			if err != nil {
				errs = append(errs, fmt.Errorf("PROXIER_TRUSTED_PROXIES: %q is not an IP address or CIDR", item))
				continue
			}
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}

	if u, err := url.Parse(c.TSControlURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("PROXIER_TS_CONTROL_URL: %q is not an http(s) URL", c.TSControlURL))
	}
	if c.ChromiumURL != "" {
		if u, err := url.Parse(c.ChromiumURL); err != nil || u.Host == "" {
			errs = append(errs, fmt.Errorf("PROXIER_CHROMIUM_URL: %q is not a URL", c.ChromiumURL))
		}
	}

	tz := get("PROXIER_TZ", "Europe/Moscow")
	if loc, err := time.LoadLocation(tz); err != nil {
		errs = append(errs, fmt.Errorf("PROXIER_TZ: unknown time zone %q", tz))
	} else {
		c.TZ = loc
	}

	if lvl, err := parseLevel(get("PROXIER_LOG_LEVEL", "info")); err != nil {
		errs = append(errs, err)
	} else {
		c.LogLevel = lvl
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// Warnings lists settings that work but are risky or turn a feature off.
func (c *Config) Warnings() []string {
	var w []string
	if c.BaseURL != nil && c.BaseURL.Scheme != "https" {
		w = append(w, "PROXIER_BASE_URL is not https: session cookies will not be Secure")
	}
	if len(c.TrustedProxies) == 0 {
		w = append(w, "PROXIER_TRUSTED_PROXIES is empty: X-Real-IP is ignored and the socket address is the client IP")
	}
	if !c.TailnetEnabled() {
		w = append(w, "PROXIER_TS_AUTHKEY is empty: the tailnet is off, routers must be reachable directly")
	}
	if c.ChromiumURL == "" {
		w = append(w, "PROXIER_CHROMIUM_URL is empty: discovery runs catalog lookup only")
	}
	return w
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not a URL", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("%q must start with https:// or http://", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%q has no host", raw)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("%q must be a bare origin without path, query or credentials", raw)
	}
	u.Path = ""
	return u, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("PROXIER_LOG_LEVEL: %q is not one of debug, info, warn, error", s)
}
