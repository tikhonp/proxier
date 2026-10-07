package remote_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
)

func TestWaitHTTPResolvePort(t *testing.T) {
	env := serverstest.NewSSHEnv(t)
	v := serverstest.NewVPS(t)
	c := env.Access(v)
	v.SetService("nginx", "running")
	step := manifest.Step{Kind: "wait-http", Args: map[string]any{"url": "https://h.example:8443/", "resolve": "127.0.0.1", "status": 200}}
	if err := remote.RunStep(context.Background(), remote.Env{}, c, stackDir, step); err != nil {
		t.Fatal(err)
	}
	var curl string
	for _, c := range v.Commands() {
		if strings.Contains(c, "curl") {
			curl = c
		}
	}
	// The port of the URL, not 443, goes into --resolve.
	if !strings.Contains(curl, "--resolve h.example:8443:127.0.0.1") {
		t.Fatalf("the request was %q", curl)
	}
	// A URL without a port uses the scheme's.
	step.Args["url"] = "https://h.example/"
	_ = remote.RunStep(context.Background(), remote.Env{}, c, stackDir, step)
	if cmds := v.Commands(); !strings.Contains(cmds[len(cmds)-1], "--resolve h.example:443:127.0.0.1") {
		t.Fatalf("the request was %q", cmds[len(cmds)-1])
	}
}

func TestWaitHTTPRetriesThenTimesOut(t *testing.T) {
	env := serverstest.NewSSHEnv(t)
	v := serverstest.NewVPS(t)
	c := env.Access(v)
	v.HTTPStatus["https://h.example/"] = "502"
	slept := 0
	e := remote.Env{Poll: time.Millisecond, Sleep: func(context.Context, time.Duration) error { slept++; time.Sleep(3 * time.Millisecond); return nil }}
	step := manifest.Step{Kind: "wait-http", Args: map[string]any{"url": "https://h.example/", "resolve": "127.0.0.1", "status": 200, "timeout": "10ms"}}
	err := remote.RunStep(context.Background(), e, c, stackDir, step)
	if err == nil || !strings.Contains(err.Error(), "did not answer 200 within 10ms (last: 502)") || slept == 0 {
		t.Fatalf("err=%v slept=%d", err, slept)
	}
	// A cancelled context ends the wait at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	step.Args["timeout"] = "1h"
	if err := remote.RunStep(ctx, remote.Env{}, c, stackDir, step); err == nil {
		t.Fatal("a cancelled wait went on")
	}
}

func TestComposeStepsAndRun(t *testing.T) {
	env := serverstest.NewSSHEnv(t)
	v := serverstest.NewVPS(t)
	c := env.Access(v)
	v.PutFile(stackDir+"/compose.yaml", []byte("services:\n  xray: {image: x}\n  nginx: {image: n}\n"))
	e := remote.Env{}
	if err := remote.RunStep(context.Background(), e, c, stackDir, manifest.Step{Kind: "compose-up", Args: map[string]any{"pull": true}}); err != nil {
		t.Fatal(err)
	}
	if got := v.Services(); got["xray"] != "running" || got["nginx"] != "running" {
		t.Fatalf("services %v", got)
	}
	var pulled bool
	for _, cmd := range v.Commands() {
		pulled = pulled || strings.Contains(cmd, " pull")
	}
	if !pulled {
		t.Error("pull: true did not pull")
	}
	if err := remote.RunStep(context.Background(), e, c, stackDir, manifest.Step{Kind: "compose-down", Args: map[string]any{"volumes": true}}); err != nil || len(v.Services()) != 0 {
		t.Fatalf("down: %v %v", err, v.Services())
	}
	// A failing compose up reports the tail of what it printed.
	v.ComposeUpFails = "Error response from daemon: port is already allocated"
	err := remote.RunStep(context.Background(), e, c, stackDir, manifest.Step{Kind: "compose-up"})
	var ee *remote.ExitError
	if err == nil || !strings.Contains(err.Error(), "port is already allocated") || !asExit(err, &ee) || ee.Code != 1 {
		t.Fatalf("up: %v", err)
	}
}

func asExit(err error, target **remote.ExitError) bool {
	for err != nil {
		if e, ok := err.(*remote.ExitError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
