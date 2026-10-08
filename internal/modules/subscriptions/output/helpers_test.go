package output_test

import (
	"fmt"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

var (
	now       = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	moscow, _ = time.LoadLocation("Europe/Moscow")
)

func ep(name, key, display string, id int64) endpoint.Endpoint {
	host := name + ".hosts.tikhonnnnn.com"
	return endpoint.Endpoint{
		Key: key, Type: endpoint.VlessXHTTPTLS, Host: host, Port: 443,
		Credential:  fmt.Sprintf("6f1c2b7e-0d3a-4c8e-9a51-%012x", id),
		Params:      map[string]string{"path": fmt.Sprintf("/%016x", id*7919), "sni": host, "mode": "stream-up", "fp": "chrome", "alpn": "h2"},
		DisplayName: display,
	}
}

func server(id int64, name, display string) output.Server {
	return output.Server{ID: id, Name: name, Health: "healthy", HealthSince: now.Add(-time.Hour), Endpoints: []endpoint.Endpoint{ep(name, "main", display, id)}}
}

func nl1() output.Server { return server(1, "nl-1", "🇳🇱 Netherlands 1") }
func de1() output.Server { return server(2, "de-1", "🇩🇪 Germany 1") }

func loc(lang i18n.Lang) *i18n.Localizer {
	c := i18n.NewCatalog()
	if err := c.Add("subscriptions", output.Messages); err != nil {
		panic(err)
	}
	return c.Localizer(lang, moscow)
}

func request(servers ...output.Server) output.Request {
	return output.Request{Link: output.Link{State: "active"}, Title: "Family", UpdateHours: 12, Format: "uri-plain", Servers: servers, Contact: "@tikhonp", Now: now}
}
