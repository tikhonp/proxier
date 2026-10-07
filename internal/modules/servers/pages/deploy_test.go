package pages_test

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

func replace(path, from, to string) func(map[string][]byte) {
	return func(files map[string][]byte) {
		if !strings.Contains(string(files[path]), from) {
			panic("no " + from + " in " + path)
		}
		files[path] = []byte(strings.Replace(string(files[path]), from, to, 1))
	}
}

func both(fs ...func(map[string][]byte)) func(map[string][]byte) {
	return func(files map[string][]byte) {
		for _, f := range fs {
			f(files)
		}
	}
}

var hashField = regexp.MustCompile(`name="hash" value="([0-9a-f]*)"`)

func TestPlanScreen(t *testing.T) {
	h, id := provisioned(t)
	base := "/servers/" + sid(id)
	v2 := h.PublishVersion(both(
		replace("site/index.html", "</body>", "<!-- v2 --></body>"),
		replace("manifest.yaml", "  - { key: container_postfix, kind: hex, length: 4 }\n", "  - { key: container_postfix, kind: hex, length: 4 }\n  - { key: stats_token, kind: hex, length: 16 }\n"),
		replace(".env", "SERVER_DOMAIN=", "STATS_TOKEN={{ .Gen.stats_token }}\nSERVER_DOMAIN="),
		replace("manifest.yaml", "fp: chrome", "fp: firefox"),
	), true)

	// Nothing differs: the screen says so and offers Force redeploy.
	body := page(t, h, base+"/redeploy")
	mustContain(t, body, "Redeploy nl-1", "Nothing to change.", "Force redeploy", "Nothing is applied yet")
	mustNotContain(t, body, "Apply upgrade")

	// The upgrade plan: files, new values, endpoint changes, steps, Apply.
	body = page(t, h, base+"/upgrade?version="+sid(int64(v2)))
	mustContain(t, body, "Upgrade nl-1 from v1 to v2", "site/index.html", ".env", "&lt;!-- v2 --&gt;",
		"New generated values", "stats_token", "new connection URI", "Endpoint main", "Upload 2 files", "Apply upgrade", "secrets masked",
		"•••stats_token•••", "•••container_postfix•••")
	values := h.Server(id)
	_ = values
	// No secret in the page: not the UUID, not the path.
	rows, _ := store.GeneratedValues(bg, h.App.DB.R, id)
	for _, r := range rows {
		v, _ := h.App.Vault.OpenString(r.Value, fmt.Sprintf("server:%d:gen:%s", id, r.Key))
		mustNotContain(t, body, v)
	}
	m := hashField.FindStringSubmatch(body)
	if m == nil || m[1] == "" {
		t.Fatalf("the Apply form has no plan hash:\n%s", excerpt(body))
	}

	// Apply goes to the server page, which follows the job.
	rec := h.Login.Post(base+"/apply", url.Values{"kind": {"upgrade"}, "version": {sid(int64(v2))}, "hash": {m[1]}})
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), base+"?job=") {
		t.Fatalf("apply: %d %s\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	body = page(t, h, rec.Header().Get("Location"))
	mustContain(t, body, "Change", "Connect", "Upload files", "Redeploy", `data-stream="/jobs/`)
	h.Drain()
	if h.Server(id).TemplateVersion != v2 {
		t.Fatalf("the server is at v%d", h.Server(id).TemplateVersion)
	}
	body = page(t, h, base)
	mustContain(t, body, "Template", "v2")
	mustNotContain(t, body, "is available")

	// Going back is a downgrade, with its warning.
	body = page(t, h, base+"/upgrade?version=1")
	mustContain(t, body, "Upgrade nl-1 from v2 to v1", "This goes from v2 back to v1.")

	// A refused apply (nothing changes) shows the screen again, queues nothing.
	rec = h.Login.Post(base+"/apply", url.Values{"kind": {"redeploy"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Nothing to change.") {
		t.Fatalf("apply of nothing: %d", rec.Code)
	}
}

func TestPlanScreenAsksForMissingParameters(t *testing.T) {
	h, id := provisioned(t)
	base := "/servers/" + sid(id)
	v2 := h.PublishVersion(replace("manifest.yaml", "generated:", "  - key: log_level\n    label: Log level\n    type: string\n    required: true\n    sample: warn\n\ngenerated:"), true)
	body := page(t, h, base+"/upgrade?version="+sid(int64(v2)))
	mustContain(t, body, "Log level", "needs values", `name="param.log_level"`, "Show the plan")
	mustNotContain(t, body, "Apply upgrade", "Steps that will run")
	// With the value, the plan appears.
	rec := h.Login.Post(base+"/plan", url.Values{"kind": {"upgrade"}, "version": {sid(int64(v2))}, "param.log_level": {"warn"}})
	if rec.Code != 200 {
		t.Fatalf("plan: %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Apply upgrade", "Steps that will run", `value="warn"`)
	// And an invalid value is refused on the field.
	rec = h.Login.Post(base+"/apply", url.Values{"kind": {"upgrade"}, "version": {sid(int64(v2))}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `name="param.log_level"`) {
		t.Fatalf("apply without the value: %d", rec.Code)
	}
}

func TestStackTab(t *testing.T) {
	h, id := provisioned(t)
	base := "/servers/" + sid(id)
	rows, _ := store.GeneratedValues(bg, h.App.DB.R, id)
	secret := map[string]string{}
	for _, r := range rows {
		v, _ := h.App.Vault.OpenString(r.Value, fmt.Sprintf("server:%d:gen:%s", id, r.Key))
		secret[r.Key] = v
	}

	body := page(t, h, base+"/stack?file=xray-config.json")
	mustContain(t, body, "Stack", "compose.yaml", "xray-config.json", "nginx/", "default.conf.template", "Deployments", "provision", "secrets masked", "•••xhttp_path•••", "Reveal")
	mustNotContain(t, body, secret["xhttp_path"], secret["client_uuid"], secret["container_postfix"])
	// The file tree marks the files that hold secrets.
	mustContain(t, body, "secrets")

	// Reveal: the real content, never cached.
	rec := h.Login.Get(base + "/stack/files/" + sid(currentDeployment(t, h, id)) + "/xray-config.json")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	mustContain(t, rec.Body.String(), secret["xhttp_path"], secret["client_uuid"])
	if rec := h.Login.Get(base + "/stack/files/" + sid(currentDeployment(t, h, id)) + "/nope.txt"); rec.Code != 404 {
		t.Errorf("a file that is not there: %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the page is cacheable")
	}

	// History: a rotation and an upgrade leave diffs that mask the old values too.
	oldUUID := secret["client_uuid"]
	v2 := h.PublishVersion(replace("site/index.html", "</body>", "<!-- v2 --></body>"), true)
	if _, err := h.Mod.Deploy.Rotate(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	target := url.Values{"kind": {"upgrade"}, "version": {sid(int64(v2))}}
	if rec := h.Login.Post(base+"/apply", target); rec.Code != 303 {
		t.Fatalf("apply: %d\n%s", rec.Code, rec.Body)
	}
	h.Drain()
	body = page(t, h, base+"/stack")
	mustContain(t, body, "rotation", "upgrade", "provision", ">Diff<")
	mustNotContain(t, body, oldUUID)

	// The diff of the upgrade against the rotation: the one file, masked.
	deps, _ := store.Deployments(bg, h.App.DB.R, id)
	body = page(t, h, base+"/stack/diff/"+sid(deps[0].ID))
	mustContain(t, body, "site/index.html", "&lt;!-- v2 --&gt;", "against the previous deployment")
	mustNotContain(t, body, oldUUID, secret["xhttp_path"])
	// The diff of the rotation against the provisioning shows what changed without the values.
	var rotation store.Deployment
	for _, d := range deps {
		if d.Kind == "rotate" {
			rotation = d
		}
	}
	body = page(t, h, base+"/stack/diff/"+sid(rotation.ID))
	mustContain(t, body, "xray-config.json", "•••client_uuid (old)•••", "•••client_uuid (new)•••")
	mustNotContain(t, body, oldUUID)
	// The first deployment has nothing to compare with.
	if rec := h.Login.Get(base + "/stack/diff/" + sid(deps[len(deps)-1].ID)); rec.Code != 404 {
		t.Errorf("diff of the first deployment: %d", rec.Code)
	}
}

func currentDeployment(t *testing.T, h *serverstest.Harness, id int64) int64 {
	t.Helper()
	d, ok, err := store.CurrentDeployment(bg, h.App.DB.R, id)
	if err != nil || !ok {
		t.Fatalf("current deployment: %v %v", ok, err)
	}
	return d.ID
}

func TestRolloutPage(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	ids := []int64{h.Provisioned(), h.AddServer("10.77.0.2"), h.AddServer("10.77.0.3")}
	h.PublishVersion(replace("site/index.html", "</body>", "<!-- v2 --></body>"), true)

	// The template page offers the upgrade of the servers on older versions.
	body := page(t, h, fmt.Sprintf("/templates/%d", h.TemplateID))
	mustContain(t, body, "Servers on older versions", "Upgrade all to v2…", "/servers/rollout/plan")

	// The plan screen.
	form := url.Values{}
	for _, id := range ids {
		form.Add("server", sid(id))
	}
	rec := h.Login.Post("/servers/rollout/plan", form)
	if rec.Code != 200 {
		t.Fatalf("plan: %d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), "Upgrade 3 servers to v2", "nl-1", "nl-2", "nl-3", "v1 → v2", "1 file changed", "Start rollout")
	mustNotContain(t, rec.Body.String(), "disabled data-action")

	// The second server fails its proxy test.
	h.ProxyAccepts(func(e endpoint.Endpoint) bool { return !strings.HasPrefix(e.Host, "nl-2.") })
	rec = h.Login.Post("/servers/rollout", form)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/servers/rollouts/") {
		t.Fatalf("start: %d %s\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	loc := rec.Header().Get("Location")
	h.WaitFor("the rollout to end", func() bool {
		ro, err := store.ListRollouts(bg, h.App.DB.R, 1)
		return err == nil && len(ro) == 1 && !ro[0].FinishedAt.IsZero()
	})
	body = page(t, h, loc)
	mustContain(t, body, "Rolling upgrade to v2", "stopped by a failure", "nl-1", "upgraded", "nl-2", "failed", "proxy test", "nl-3", "not started",
		"1 done · 1 failed · 1 not started")
	mustNotContain(t, body, `hx-trigger="every 2s"`, "Stop rollout")
	if rec := h.Login.Get("/servers/rollouts/9999"); rec.Code != 404 {
		t.Errorf("unknown rollout: %d", rec.Code)
	}

	// A running rollout polls and offers Stop.
	h.ProxyAccepts(nil)
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	// nl-1 is at v2 already: the rest of the fleet goes.
	rec = h.Login.Post("/servers/rollout", form)
	if rec.Code != 303 {
		t.Fatalf("second start: %d\n%s", rec.Code, rec.Body)
	}
	<-reached
	body = page(t, h, rec.Header().Get("Location"))
	mustContain(t, body, `hx-trigger="every 2s"`, "Stop rollout", "upgrading", "skipped", "already at this version")
	if rec := h.Login.Post(rec.Header().Get("Location")+"/stop", nil); rec.Code != 303 {
		t.Fatalf("stop: %d", rec.Code)
	}
	release()
	h.Drain()
}

func TestRotateDialogWithoutSubscriptions(t *testing.T) {
	h, id := provisioned(t)
	base := "/servers/" + sid(id)
	body := page(t, h, base+"/rotate")
	mustContain(t, body, "Rotate the credentials of nl-1", "client_uuid", "xhttp_path", "Subscriptions aren&#39;t built yet; no links are affected.",
		"Apps refresh within their update interval", "Rotate")
	mustNotContain(t, body, "container_postfix")
	rec := h.Login.Post(base+"/rotate", nil)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), base+"?job=") {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	h.Drain()
	if n := len(h.Events("server.credentials_rotated")); n != 1 {
		t.Errorf("%d rotations", n)
	}
}

func TestServerActionsOnThePage(t *testing.T) {
	h, id := provisioned(t)
	base := "/servers/" + sid(id)
	body := page(t, h, base)
	mustContain(t, body, "Actions", "Redeploy…", "Edit parameters…", "Rotate credentials…", "Restart stack", "Update images", "Reboot", "Container logs",
		base+"/redeploy", base+"/params", base+"/rotate", base+"/restart", base+"/images", base+"/reboot", base+"/logs", `href="`+base+`/stack"`)
	mustNotContain(t, body, "Upgrade…", "Roll back…") // one version only, nothing failed

	// The light operations queue a job and go back to the page that follows it.
	for _, op := range []string{"restart", "images", "reboot"} {
		rec := h.Login.Post(base+"/"+op, nil)
		if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), base+"?job=") {
			t.Fatalf("%s: %d %s", op, rec.Code, rec.Header().Get("Location"))
		}
		h.Drain()
	}
	rec := h.Login.Post(base+"/logs", nil)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/jobs/") {
		t.Fatalf("logs: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	h.Drain()
	body = page(t, h, rec.Header().Get("Location"))
	mustContain(t, body, "Container logs")

	// A failed deployment offers Roll back on the page and in the Stack tab.
	v2 := h.PublishVersion(replace("site/index.html", "</body>", "<!-- v2 --></body>"), true)
	h.VPS.ComposeUpFails = "bad image"
	if rec := h.Login.Post(base+"/apply", url.Values{"kind": {"upgrade"}, "version": {sid(int64(v2))}}); rec.Code != 303 {
		t.Fatalf("apply: %d", rec.Code)
	}
	h.Drain()
	body = page(t, h, base)
	mustContain(t, body, "Roll back…", "The last change (upgrade) failed.", "bad image", "Upgrade…")
	body = page(t, h, base+"/rollback")
	mustContain(t, body, "Roll nl-1 back to v1", "Apply roll back", "uploaded again")
	body = page(t, h, base+"/stack")
	mustContain(t, body, "Roll back…")

	// Only an active server can be changed.
	if rec := h.Login.Get("/servers/9999/redeploy"); rec.Code != 404 {
		t.Errorf("redeploy of nothing: %d", rec.Code)
	}
}
