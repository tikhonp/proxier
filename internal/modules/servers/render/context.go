// Package render turns a template version into the files and steps of one
// server (docs/modules/servers.md#rendering): Go templates with missing keys
// as errors and a small function map. It is pure: nothing here reads a
// server, the database or the clock.
package render

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/tikhonp/proxier/internal/modules/servers/gen"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

type Location struct{ Code, Name, Country string }

type Server struct {
	Name                              string
	Number                            int
	Location                          Location
	IP                                string
	SSHPort                           int
	ManagementHostname, ProxyHostname string
}

// Client is an identity the stack must accept (ADR 0004).
type Client struct{ ID, Name string }

type Template struct {
	Slug    string
	Version int
}

// Context is what templates see: .Server .Params .Gen .Clients .Template.
type Context struct {
	Server   Server
	Params   map[string]any
	Gen      map[string]string
	Clients  []Client
	Template Template
}

// SharedClient is the name of the one client of the first version.
const SharedClient = "shared"

// sampleUUID stands in for the credential when a manifest has no endpoint
// to take it from.
const sampleUUID = "00000000-0000-4000-8000-000000000000"

// SampleContext is the context validation and preview render with: server
// xx-1 in location xx, documentation addresses, parameters from their sample
// (or default), and gen for the generated values (created when nil or
// incomplete).
func SampleContext(m *manifest.Manifest, slug string, genValues map[string]string) Context {
	params := map[string]any{}
	for _, p := range m.Parameters {
		raw := ""
		switch {
		case p.Sample != nil:
			raw = *p.Sample
		case p.Default != nil:
			raw = *p.Default
		}
		params[p.Key] = ParamValue(p, raw)
	}
	all, _, err := gen.Ensure(m.Generated, genValues)
	if err != nil {
		// An invalid declaration is reported by manifest.Verify; keep what
		// could be made so rendering reports the keys it misses.
		all = map[string]string{}
		for k, v := range genValues {
			all[k] = v
		}
	}
	c := Context{
		Server: Server{
			Name: "xx-1", Number: 1, IP: "192.0.2.10", SSHPort: 22,
			Location:           Location{Code: "xx", Name: "Sample", Country: "XX"},
			ManagementHostname: "xx-1.hosts.example.invalid", ProxyHostname: "xx-1.hosts.example.invalid",
		},
		Params:   params,
		Gen:      all,
		Template: Template{Slug: slug},
	}
	if !hasCredential(m) {
		id := sampleUUID
		if v, ok := all["client_uuid"]; ok {
			id = v
		} else if u, err := uuid.NewRandom(); err == nil {
			id = u.String()
		}
		c.Clients = []Client{{ID: id, Name: SharedClient}}
	}
	return c
}

func hasCredential(m *manifest.Manifest) bool {
	for _, e := range m.Endpoints {
		if strings.TrimSpace(e.Credential) != "" {
			return true
		}
	}
	return false
}

// ParamValue types a stored parameter value for templates: int and bool
// parameters are numbers and booleans, the rest strings.
func ParamValue(p manifest.Parameter, raw string) any {
	switch p.Type {
	case "int":
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n
		}
		return int64(0)
	case "bool":
		return raw == "true"
	}
	return raw
}

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Hostname renders the hostname pattern of Settings ("{location}-{number}.hosts.example.com").
// The pattern must hold both placeholders and the result must be a valid DNS name.
func Hostname(pattern, location string, number int) (string, error) {
	if !strings.Contains(pattern, "{location}") || !strings.Contains(pattern, "{number}") {
		return "", errors.New("the pattern must contain {location} and {number}")
	}
	h := strings.NewReplacer("{location}", location, "{number}", strconv.Itoa(number)).Replace(pattern)
	if len(h) > 253 {
		return "", errors.New("the hostname is longer than 253 characters")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("%q is not a fully qualified name", h)
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return "", fmt.Errorf("%q is not a valid hostname", h)
		}
	}
	return h, nil
}
