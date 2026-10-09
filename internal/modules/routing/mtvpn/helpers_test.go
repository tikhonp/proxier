package mtvpn_test

import (
	"context"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/mtvpn"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func bg() context.Context { return context.Background() }

const base = "[General]\ndns-server = system\n\n[Rule]\nRULE-SET,https://example.com/ads.list,REJECT\nFINAL,DIRECT\n"

const password = "s3cr3t-Pa55-xyzzy"

// today serves the shape of today's mtvpn.yaml and returns its text: one
// service list, one raw-URL service, a Shadowrocket base, upload credentials.
func today(h *routingtest.Harness) string {
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	h.Up.V2fly("youtube", "youtube.com\nytimg.com\n")
	h.Up.V2fly("google", "google.com\n")
	h.Up.Site("main", "social", "instagram.com", "instagram.com", "cdninstagram.com")
	tunneled := h.Up.File("tunneled-domains.txt", "# my domains\nexample.com\nfull:api.example.org\nwww.example.com\n")
	list := h.Up.File("lists/mtvpn-main.txt", "# main\n- v2fly:youtube\n- google\n- iplist:instagram.com\n- v2fly:anthropic\n")
	b := h.Up.File("base.conf", base)
	return "router: 192.168.88.1\nservices:\n  - anthropic\n  - " + tunneled + "\nservice_lists:\n  - " + list +
		"\nshadowrocket_base: " + b + "\nshadowrocket_upload: https://copyparty.example/up/\nshadowrocket_user: tikhon\nshadowrocket_password: " + password + "\n"
}

func preview(t *testing.T, h *routingtest.Harness, text string) mtvpn.Import {
	t.Helper()
	id, err := h.Mod.Import.Preview(bg(), text, "admin")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	im, err := h.Mod.Import.Get(bg(), id)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

// all ticks every importable row, keeps URL rows as URL sources and takes
// the offered config as it is.
func all(im mtvpn.Import) mtvpn.Choices {
	c := mtvpn.Choices{Include: map[int]bool{}, Convert: map[int]bool{}, ListID: im.ListID,
		CreateConfig: im.Shadowrocket.Create, ConfigName: im.Shadowrocket.Name, ConfigList: im.Shadowrocket.ListID}
	for i, r := range im.Rows {
		c.Include[i] = r.Include
	}
	return c
}

// run imports with the choices and waits for the job.
func run(t *testing.T, h *routingtest.Harness, im mtvpn.Import, c mtvpn.Choices) mtvpn.Import {
	t.Helper()
	if _, err := h.Mod.Import.Run(bg(), im.ID, c, "admin"); err != nil {
		t.Fatalf("run: %v", err)
	}
	h.StartJobs()
	h.Drain()
	out, err := h.Mod.Import.Get(bg(), im.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func tags(t *testing.T, h *routingtest.Harness, listID int64) []string {
	t.Helper()
	v, err := h.Mod.Lists.View(bg(), listID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range v.Members {
		out = append(out, m.Service.Tag)
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
