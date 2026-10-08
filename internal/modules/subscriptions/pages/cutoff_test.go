package pages_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

func TestCutOffPages(t *testing.T) {
	h := substest.New(t)
	h.Catalog.Put(substest.Server(3, "fi-1", "🇫🇮", "Finland", 1))
	h.Catalog.SetHealth(2, "blocked", substest.HealthySince)
	h.Rotator.Refuse(3, servers.ErrNothingToRotate)
	family := h.Subscription("Family", 1, 2, 3)
	id, _ := h.Link(family, "Alex")
	h.Link(family, "Mom")
	h.Link(h.Subscription("Mine", 2), "Me")
	href := fmt.Sprintf("/links/%d", id)

	// The link page offers Cut off… in its Actions; no cut-off yet.
	body := page(t, h, href)
	mustContain(t, body, "Cut off…", `href="`+href+`/cutoff"`)
	mustNotContain(t, body, `id="cutoff"`)

	// The plan: rotated and skipped servers, the other links, the warning.
	body = page(t, h, href+"/cutoff")
	mustContain(t, body, "Cut off Alex", "The link is disabled at once", "Servers it rotates",
		">nl-1<", ">Healthy<", ">de-1<", ">Blocked<", "Skipped", ">fi-1<", "its template has no rotatable values",
		"2 other links in 2 subscriptions get new URIs on their next refresh.",
		"Apps refresh within their update interval (12 h by default).", `action="`+href+`/cutoff"`)
	noInline(t, body)
	if l, _ := h.Mod.Links.Get(t.Context(), id); l.State != "active" {
		t.Errorf("the plan changed the link: %s", l.State)
	}

	// Cut off → the link page, disabled, its area polling while a rotation runs.
	rec := h.Login.Post(href+"/cutoff", url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != href {
		t.Fatalf("cut off: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body = page(t, h, href)
	mustContain(t, body, `id="cutoff"`, `hx-get="`+href+`/cutoff/status"`, `hx-trigger="every 2s"`,
		"Cut-off", ">rotating<", ">waiting<", ">skipped<", "its template has no rotatable values · still has the old credential", "Disabled")
	noInline(t, body)

	// The first rotation fails, the second runs: the status shows Retry and
	// still polls.
	c, _, _ := h.Mod.Links.LatestCutOff(t.Context(), id)
	h.RotationFailed(c.Items[0].JobID, 1, "ssh: connection refused")
	status := h.Login.Get(href + "/cutoff/status")
	if status.Code != http.StatusOK {
		t.Fatalf("status: %d", status.Code)
	}
	mustContain(t, status.Body.String(), ">failed<", "ssh: connection refused · still has the old credential",
		`action="`+href+`/cutoff/1/retry"`, ">Retry<", ">rotating<", `hx-trigger="every 2s"`)

	// The second ends: nothing waits or runs, so the answer stops polling.
	c, _, _ = h.Mod.Links.LatestCutOff(t.Context(), id)
	h.Rotated(c.Items[1].JobID, 2)
	status = h.Login.Get(href + "/cutoff/status")
	mustContain(t, status.Body.String(), ">rotated<", ">failed<", "/cutoff/1/retry")
	mustNotContain(t, status.Body.String(), "hx-trigger")

	// Retry: back to rotating; a server that didn't fail can't be retried.
	if rec := h.Login.Post(href+"/cutoff/2/retry", url.Values{}); rec.Code != http.StatusConflict {
		t.Errorf("retrying a rotated server: %d", rec.Code)
	}
	if rec := h.Login.Post(href+"/cutoff/1/retry", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("retry: %d", rec.Code)
	}
	mustContain(t, h.Login.Get(href+"/cutoff/status").Body.String(), `hx-trigger="every 2s"`, ">rotating<")

	// The alert band offers Cut off too, naming the servers it rotates.
	alex2, _ := h.Link(family, "Alex 2")
	shared(t, h, alex2, 6)
	body = page(t, h, fmt.Sprintf("/links/%d", alex2))
	mustContain(t, body, "Cut off Alex 2…", "Disables the link and rotates nl-1, de-1, so copied URIs stop working too.")

	// A deleted link: 409.
	if err := h.Mod.Links.Delete(t.Context(), alex2, "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := h.Login.Get(fmt.Sprintf("/links/%d/cutoff", alex2)); rec.Code != http.StatusConflict {
		t.Errorf("plan of a deleted link: %d", rec.Code)
	}
	if rec := h.Login.Post(fmt.Sprintf("/links/%d/cutoff", alex2), url.Values{}); rec.Code != http.StatusConflict {
		t.Errorf("cut off of a deleted link: %d", rec.Code)
	}

	// Nothing to rotate: Cut off only disables the link.
	only, _ := h.Link(h.Subscription("Only fi", 3), "Solo")
	mustContain(t, page(t, h, fmt.Sprintf("/links/%d/cutoff", only)), "Nothing to rotate: Cut off only disables the link.")

	// Without a rotator no Cut off is offered anywhere.
	h2 := substest.New(t, substest.NoRotator())
	id2, _ := h2.Link(h2.Subscription("Family", 1), "Alex")
	shared(t, h2, id2, 6)
	body = page(t, h2, fmt.Sprintf("/links/%d", id2))
	mustContain(t, body, "Shared-link alert")
	mustNotContain(t, body, "Cut off", "/cutoff")
	if rec := h2.Login.Get(fmt.Sprintf("/links/%d/cutoff", id2)); rec.Code != http.StatusNotFound {
		t.Errorf("plan without a rotator: %d", rec.Code)
	}
}
