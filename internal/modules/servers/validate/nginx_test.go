package validate_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
)

const nginxPath = "nginx/default.conf.template"

func TestSeedNginxPasses(t *testing.T) {
	r := run(t, seedWith(nil))
	for _, f := range r.Findings {
		if f.Check == "nginx" {
			t.Errorf("the seed's nginx file must pass: %s", f)
		}
	}
	// ${SERVER_DOMAIN} is expanded from the compose service's environment;
	// where the service is not given it, it stays as written and still parses
	compose := string(seedWith(nil)["compose.yaml"])
	compose = strings.Replace(compose, "      - SERVER_DOMAIN=$SERVER_DOMAIN\n", "", 1)
	r = run(t, seedWith(map[string]string{"compose.yaml": compose}))
	for _, f := range r.Findings {
		if f.Check == "nginx" {
			t.Errorf("an undefined variable is left as written and must still parse: %s", f)
		}
	}
	// given an empty value, server_name has no argument
	compose = strings.Replace(string(seedWith(nil)["compose.yaml"]), "SERVER_DOMAIN=$SERVER_DOMAIN", "SERVER_DOMAIN=", 1)
	r = run(t, seedWith(map[string]string{"compose.yaml": compose}))
	if !has(r, finding.Error, "nginx", nginxPath, 30, "server_name") {
		t.Errorf("an empty domain must fail server_name: %v", r.Findings)
	}
}

func TestNginxErrors(t *testing.T) {
	src := string(seedWith(nil)[nginxPath])

	// a missing ';' on the "server_tokens off;" line (line 4)
	broken := strings.Replace(src, "server_tokens off;", "server_tokens off", 1)
	r := run(t, seedWith(map[string]string{nginxPath: broken}))
	if !has(r, finding.Error, "nginx", nginxPath, 0, "") {
		t.Fatalf("a missing semicolon must be an error: %v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Check == "nginx" && f.Line == 0 {
			t.Errorf("a syntax error must carry its line: %s", f)
		}
	}

	// an unknown directive on its line
	unknown := strings.Replace(src, "server_tokens off;", "server_tokenz off;", 1)
	r = run(t, seedWith(map[string]string{nginxPath: unknown}))
	if !has(r, finding.Error, "nginx", nginxPath, 4, "server_tokenz") {
		t.Errorf("an unknown directive must be an error on line 4: %v", r.Findings)
	}

	// a directive in the wrong context: listen directly in http
	wrong := strings.Replace(src, "server_tokens off;", "listen 80;", 1)
	r = run(t, seedWith(map[string]string{nginxPath: wrong}))
	if !has(r, finding.Error, "nginx", nginxPath, 4, "listen") {
		t.Errorf("a directive in the wrong context must be an error: %v", r.Findings)
	}

	// the wrong number of arguments
	args := strings.Replace(src, "server_tokens off;", "server_tokens;", 1)
	r = run(t, seedWith(map[string]string{nginxPath: args}))
	if !has(r, finding.Error, "nginx", nginxPath, 4, "server_tokens") {
		t.Errorf("the wrong argument count must be an error: %v", r.Findings)
	}
}

func TestNginxContextFromMount(t *testing.T) {
	// a server block under /etc/nginx/templates/ is inside http: it passes
	r := run(t, seedWith(nil))
	if !r.OK() {
		t.Fatalf("seed: %v", r.Findings)
	}
	// mounted as the main config, the same file has server outside http
	compose := strings.Replace(string(seedWith(nil)["compose.yaml"]),
		"/etc/nginx/templates/default.conf.template", "/etc/nginx/nginx.conf", 1)
	r = run(t, seedWith(map[string]string{"compose.yaml": compose}))
	if !has(r, finding.Error, "nginx", nginxPath, 0, "server") {
		t.Errorf("a server block as nginx.conf must fail: %v", r.Findings)
	}
	// a file no service mounts, named otherwise, is read inside http
	noMount := strings.Replace(string(seedWith(nil)["compose.yaml"]),
		`      - "./nginx/default.conf.template:/etc/nginx/templates/default.conf.template:ro"`+"\n", "", 1)
	r = run(t, seedWith(map[string]string{"compose.yaml": noMount}))
	for _, f := range r.Findings {
		if f.Check == "nginx" {
			t.Errorf("an unmounted file is wrapped in http and passes: %s", f)
		}
	}
}
