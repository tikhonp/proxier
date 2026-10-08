package output_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func build(t *testing.T, r output.Request) output.Response {
	t.Helper()
	resp, err := output.Build(loc(i18n.EN), r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestBuildServesURIsInOrder(t *testing.T) {
	resp := build(t, request(nl1(), de1()))
	if resp.Outcome != output.OK || len(resp.Lines) != 2 {
		t.Fatalf("%s %v", resp.Outcome, resp.Lines)
	}
	for i, name := range []string{"#%F0%9F%87%B3%F0%9F%87%B1%20Netherlands%201", "#%F0%9F%87%A9%F0%9F%87%AA%20Germany%201"} {
		if !strings.HasPrefix(resp.Lines[i], "vless://") || !strings.HasSuffix(resp.Lines[i], name) {
			t.Errorf("line %d: %s", i, resp.Lines[i])
		}
	}
	if string(resp.Body) != resp.Lines[0]+"\n"+resp.Lines[1]+"\n" {
		t.Errorf("body %q", resp.Body)
	}
	if len(resp.Served) != 2 || len(resp.Hidden) != 0 || resp.AllHidden {
		t.Errorf("served %d hidden %d", len(resp.Served), len(resp.Hidden))
	}
	if _, err := output.Build(loc(i18n.EN), output.Request{Format: "mihomo"}); err != output.ErrBadFormat {
		t.Errorf("unknown format: %v", err)
	}
}

func TestEveryEndpointIsServed(t *testing.T) {
	s := nl1()
	s.Endpoints = append(s.Endpoints, ep("nl-1", "backup", "🇳🇱 Netherlands 1 · backup", 9))
	resp := build(t, request(s, de1()))
	if len(resp.Lines) != 3 || !strings.HasSuffix(resp.Lines[0], "Netherlands%201") ||
		!strings.HasSuffix(resp.Lines[1], "backup") || !strings.HasSuffix(resp.Lines[2], "Germany%201") {
		t.Fatalf("lines: %v", resp.Lines)
	}
}

func TestStubPrecedence(t *testing.T) {
	past := now.Add(-time.Hour)
	for _, c := range []struct {
		link    output.Link
		servers []output.Server
		want    string
	}{
		{output.Link{State: "deleted", Expires: past}, nil, output.StubDeleted},
		{output.Link{State: "disabled", Expires: past}, []output.Server{nl1()}, output.StubDisabled},
		{output.Link{State: "active", Expires: past}, nil, output.StubExpired},
		{output.Link{State: "active", Expires: now}, []output.Server{nl1()}, output.StubExpired}, // the first expired moment
		{output.Link{State: "active", Expires: now.Add(time.Second)}, nil, output.StubEmpty},
		{output.Link{State: "active"}, []output.Server{nl1()}, output.OK},
	} {
		r := request(c.servers...)
		r.Link = c.link
		resp := build(t, r)
		if resp.Outcome != c.want {
			t.Errorf("%+v: %s, want %s", c.link, resp.Outcome, c.want)
		}
		if c.want != output.OK && (len(resp.Lines) != 1 || !strings.HasPrefix(resp.Lines[0], "vless://00000000-0000-0000-0000-000000000000@127.0.0.1:1?")) {
			t.Errorf("%s: lines %v", c.want, resp.Lines)
		}
		if len(resp.Headers) != 4 {
			t.Errorf("%s: a stub has the headers too", c.want)
		}
	}
	r := request(nl1())
	r.Link = output.Link{State: "disabled", Expires: past}
	if resp := build(t, r); !strings.HasSuffix(resp.Lines[0], output.Escape("⛔ Link disabled · contact @tikhonp")) {
		t.Errorf("disabled and expired: %s", resp.Lines[0])
	}
}

func TestEmptySubscriptionStub(t *testing.T) {
	resp := build(t, request())
	if resp.Outcome != output.StubEmpty || resp.Lines[0] != output.StubURI("⚠️ No servers yet") {
		t.Fatalf("%s %v", resp.Outcome, resp.Lines)
	}
	// a member without endpoints serves nothing either
	s := nl1()
	s.Endpoints = []endpoint.Endpoint{}
	if resp := build(t, request(s)); resp.Outcome != output.StubEmpty {
		t.Errorf("no endpoints: %s", resp.Outcome)
	}
}

func TestMaskedBuildHidesCredentials(t *testing.T) {
	r := request(nl1(), de1())
	r.Masked = true
	resp := build(t, r)
	for i, s := range []output.Server{nl1(), de1()} {
		line := resp.Lines[i]
		e := s.Endpoints[0]
		if strings.Contains(line, e.Credential) || strings.Contains(line, strings.TrimPrefix(e.Params["path"], "/")) {
			t.Errorf("a secret in %s", line)
		}
		if !strings.HasPrefix(line, "vless://••••••••@"+e.Host+":443?") || !strings.Contains(line, "&path=%2F••••••••&") {
			t.Errorf("masked line: %s", line)
		}
	}
	if strings.Contains(string(resp.Body), nl1().Endpoints[0].Credential) {
		t.Error("a credential in the body")
	}
}
