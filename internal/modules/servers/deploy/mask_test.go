package deploy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// A plan never holds a generated value or a secret parameter, in either of
// its sides.
func TestPlansAreMasked(t *testing.T) {
	h, id := stubbed(t)
	token := "tok-5f2a9c0d-secret-parameter"
	v2 := h.PublishVersion(all(
		addParam("api_token", true, "    secret: true\n"),
		change("manifest.yaml", "  - { key: container_postfix, kind: hex, length: 4 }\n", "  - { key: container_postfix, kind: hex, length: 4 }\n  - { key: stats_token, kind: hex, length: 16 }\n"),
		change(".env", "SERVER_DOMAIN=", "API_TOKEN={{ .Params.api_token }}\nSTATS_TOKEN={{ .Gen.stats_token }}\nSERVER_DOMAIN="),
		// context lines around the UUID and the path in the xray config
		change("xray-config.json", `"mode": "auto"`, `"mode": "stream-up"`),
	), true)
	values, _ := generated(t, h, id)

	assertMasked := func(plan deploy.Plan, secrets ...string) {
		t.Helper()
		b, _ := json.Marshal(plan)
		text := string(b)
		for _, f := range plan.Files {
			text += "\n" + f.Diff
		}
		for _, s := range secrets {
			if s != "" && strings.Contains(text, s) {
				t.Errorf("the plan holds %q", s)
			}
		}
	}

	target := deploy.Target{Version: v2, Params: map[string]string{"api_token": token}}
	plan, err := h.Mod.Deploy.Plan(bg, id, target)
	if err != nil {
		t.Fatal(err)
	}
	assertMasked(plan, token, values["client_uuid"], values["xhttp_path"])
	var env, xray string
	for _, f := range plan.Files {
		switch f.Path {
		case ".env":
			env = f.Diff
		case "xray-config.json":
			xray = f.Diff
		}
	}
	if !strings.Contains(env, "+API_TOKEN=•••api_token•••") || !strings.Contains(env, "+STATS_TOKEN=•••stats_token•••") {
		t.Errorf(".env diff:\n%s", env)
	}
	// The unchanged secrets around a changed line are masked too.
	if !strings.Contains(xray, `"path": "•••xhttp_path•••"`) {
		t.Errorf("xray-config.json diff:\n%s", xray)
	}

	mustState(t, h, apply(t, h, id, target), jobs.Succeeded)
	values, _ = generated(t, h, id)

	// Editing the secret parameter: the diff shows that it changed, not what it was.
	changed := deploy.Target{Kind: deploy.KindParams, Params: map[string]string{"api_token": "tok-other-0000-secret-parameter"}}
	plan, err = h.Mod.Deploy.Plan(bg, id, changed)
	if err != nil {
		t.Fatal(err)
	}
	assertMasked(plan, token, "tok-other-0000-secret-parameter", values["client_uuid"], values["xhttp_path"], values["stats_token"])
	if len(plan.Files) != 1 || plan.Files[0].Path != ".env" || plan.Files[0].SecretOnly {
		t.Fatalf("plan files: %+v", plan.Files)
	}
	d := plan.Files[0].Diff
	if !strings.Contains(d, "-API_TOKEN=•••api_token (old)•••") || !strings.Contains(d, "+API_TOKEN=•••api_token (new)•••") {
		t.Errorf("diff of the changed secret:\n%s", d)
	}
	if len(plan.ParamsChanged) != 1 || plan.ParamsChanged[0] != "api_token" {
		t.Errorf("changed parameters: %v", plan.ParamsChanged)
	}

	// After a rotation an old deployment's files still hold the old UUID, which
	// the server no longer knows: its own record of what it was rendered with
	// keeps it masked.
	oldUUID := values["client_uuid"]
	rotate(t, h, id)
	h.Drain()
	again, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	assertMasked(again, oldUUID, values["xhttp_path"], token)
}

func TestMaskerHidesTheLongestValueWhole(t *testing.T) {
	s := deploy.Secrets{}
	s.Add("token", "abc")
	s.Add("long", "abcdef-123")
	s.Add("empty", "")
	got := string(deploy.Mask(s).Apply([]byte("x=abcdef-123 y=abc z=")))
	if got != "x=•••long••• y=•••token••• z=" {
		t.Errorf("masked: %q", got)
	}
	// The same key with another value on the other side is told apart.
	old, cur := deploy.Secrets{}, deploy.Secrets{}
	old.Add("k", "old-value")
	cur.Add("k", "new-value")
	if got := string(deploy.NewMasker(old, cur, "old").Apply([]byte("old-value"))); got != "•••k (old)•••" {
		t.Errorf("old side: %q", got)
	}
	if got := string(deploy.NewMasker(cur, old, "new").Apply([]byte("new-value"))); got != "•••k (new)•••" {
		t.Errorf("new side: %q", got)
	}
	if got := string(deploy.NewMasker(cur, cur, "new").Apply([]byte("new-value"))); got != "•••k•••" {
		t.Errorf("unchanged: %q", got)
	}
}
